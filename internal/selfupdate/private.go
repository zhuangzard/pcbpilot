package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// A private release repository answers anonymous requests with 404, and its
// asset download URLs never accept a token: assets are only served through the
// REST API. releaseGet therefore retries a 404 on a release-asset URL through
// the API, with a token from GH_TOKEN / GITHUB_TOKEN or the gh CLI. The token
// goes to the API host only, never to a mirror or a custom release server.

// Hosts and the gh lookup, overridable in tests.
var (
	githubWebBase = "https://github.com"
	githubAPIBase = "https://api.github.com"
	ghCLIToken    = func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	ghTokenOnce  sync.Once
	ghTokenValue string
)

const privateRepoHint = "if the release repository is private, run `gh auth login` or set GH_TOKEN"

// authToken is the env token, else the gh CLI's (asked once per process).
func authToken() string {
	if t := githubToken(); t != "" {
		return t
	}
	ghTokenOnce.Do(func() { ghTokenValue = ghCLIToken() })
	return ghTokenValue
}

// splitReleaseAssetURL reads tag and asset out of a release download URL of
// the effective repository on GitHub; ok is false for any other URL.
func splitReleaseAssetURL(url string) (tag, asset string, ok bool) {
	rest, found := strings.CutPrefix(url, githubWebBase+"/"+Repo()+"/releases/download/")
	if !found {
		return "", "", false
	}
	tag, asset, found = strings.Cut(rest, "/")
	if !found || tag == "" || asset == "" || strings.ContainsAny(asset, "/?#") {
		return "", "", false
	}
	return tag, asset, true
}

func notFoundResponse(req *http.Request) *http.Response {
	return &http.Response{
		Status: "404 Not Found", StatusCode: http.StatusNotFound, Request: req,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")),
	}
}

// releaseGet fetches url. An anonymous 404 on a release-asset URL is retried
// through the API when a token is available; the reply is then the asset body,
// or a 404 when the release really has no such asset.
func releaseGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNotFound {
		return resp, err
	}
	tag, asset, ok := splitReleaseAssetURL(url)
	if !ok {
		return resp, nil
	}
	token := authToken()
	if token == "" {
		return resp, nil
	}
	resp.Body.Close()

	api := func(path, accept string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/repos/"+Repo()+path, nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("Accept", accept)
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultClient.Do(r)
	}
	rel, err := api("/releases/tags/"+tag, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer rel.Body.Close()
	if rel.StatusCode == http.StatusNotFound {
		return notFoundResponse(req), nil
	}
	if rel.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github release %s: %s", tag, rel.Status)
	}
	var body struct {
		Assets []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(rel.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("github release %s: %w", tag, err)
	}
	for _, a := range body.Assets {
		if a.Name == asset {
			return api(fmt.Sprintf("/releases/assets/%d", a.ID), "application/octet-stream")
		}
	}
	return notFoundResponse(req), nil
}

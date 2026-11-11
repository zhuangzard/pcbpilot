package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// privateRepo serves a repository whose web URLs are 404 and whose API needs
// the token "tok". It records every Authorization header seen off the API.
func privateRepo(t *testing.T) (leaked *[]string) {
	t.Helper()
	var seen []string
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			seen = append(seen, a)
		}
		http.NotFound(w, r)
	}))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
		case "/repos/o/r/releases/tags/v1.2.3":
			fmt.Fprint(w, `{"assets":[{"id":7,"name":"checksums.txt"}]}`)
		case "/repos/o/r/releases/assets/7":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "accept", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, "body-7")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(web.Close)
	t.Cleanup(api.Close)
	oldWeb, oldAPI, oldGH := githubWebBase, githubAPIBase, ghCLIToken
	githubWebBase, githubAPIBase = web.URL, api.URL
	ghTokenOnce, ghTokenValue = sync.Once{}, ""
	t.Cleanup(func() {
		githubWebBase, githubAPIBase, ghCLIToken = oldWeb, oldAPI, oldGH
		ghTokenOnce, ghTokenValue = sync.Once{}, ""
	})
	t.Setenv(RepoEnv, "o/r")
	t.Setenv(ReleaseBaseEnv, "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	return &seen
}

func TestReleaseGetPrivateRepo(t *testing.T) {
	leaked := privateRepo(t)
	ctx := context.Background()
	get := func(url string) (int, string) {
		t.Helper()
		resp, err := releaseGet(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	asset := ReleaseAssetURL("1.2.3", "checksums.txt")

	// No token anywhere: the 404 stands.
	ghCLIToken = func() string { return "" }
	if code, _ := get(asset); code != http.StatusNotFound {
		t.Fatalf("anonymous: %d", code)
	}
	// The gh CLI's token reaches the asset through the API.
	ghTokenOnce, ghTokenValue = sync.Once{}, ""
	ghCLIToken = func() string { return "tok" }
	if code, body := get(asset); code != http.StatusOK || body != "body-7" {
		t.Fatalf("gh token: %d %q", code, body)
	}
	// An asset the release does not list, and a missing release, stay 404.
	if code, _ := get(ReleaseAssetURL("1.2.3", "mcp.tar.gz")); code != http.StatusNotFound {
		t.Fatalf("unlisted asset: %d", code)
	}
	if code, _ := get(ReleaseAssetURL("9.9.9", "checksums.txt")); code != http.StatusNotFound {
		t.Fatalf("missing release: %d", code)
	}
	// A URL that is not a release asset of this repository is never retried.
	if code, _ := get(githubWebBase + "/other/repo/releases/download/v1.2.3/checksums.txt"); code != http.StatusNotFound {
		t.Fatalf("foreign repo: %d", code)
	}
	// The environment token wins over the CLI.
	t.Setenv("GH_TOKEN", "tok")
	ghCLIToken = func() string { t.Fatal("gh asked although GH_TOKEN is set"); return "" }
	if code, _ := get(asset); code != http.StatusOK {
		t.Fatalf("env token: %d", code)
	}
	if len(*leaked) != 0 {
		t.Fatalf("token sent off the API host: %v", *leaked)
	}
}

func TestLatestReleaseVersionPrivateRepo(t *testing.T) {
	privateRepo(t)
	ctx := context.Background()
	ghCLIToken = func() string { return "" }
	if _, err := LatestReleaseVersion(ctx); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("anonymous: %v", err)
	}
	ghTokenOnce, ghTokenValue = sync.Once{}, ""
	ghCLIToken = func() string { return "tok" }
	if v, err := LatestReleaseVersion(ctx); err != nil || v != "1.2.3" {
		t.Fatalf("gh token: %q %v", v, err)
	}
}

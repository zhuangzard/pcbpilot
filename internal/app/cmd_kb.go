package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/pkg/kb"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

// newKBCmd is `pcbpilot kb`: the per-project resource library.
func newKBCmd(stdout, stderr io.Writer) *cobra.Command {
	var dir string
	k := &cobra.Command{
		Use:   "kb",
		Short: "Per-project resource library: add papers/datasheets/standards/requirements/mech files, search (BM25), cite",
		Long: `Stores reference material under <work dir>/resources/<kind>/ and indexes it under
<work dir>/.pcbpilot-kb/ (text extraction → page-bounded chunks → BM25, pure Go; PDF via a
pure-Go reader with pdftotext as fallback when installed; md/txt/csv/json/html/docx).

Work in three steps and never load the whole library into context:
  1. search   pcbpilot kb search "LDO dropout thermal" --docs   (screen: which documents)
  2. narrow   pcbpilot kb search "dropout" --doc <id>           (which pages)
  3. read     pcbpilot kb show <id> --pages 3-4                 (deep-read only what you cite)
Cite as kb:<id>#p<page>. After deep-reading a document, fill its summary slot with
'kb set-summary' so later searches can screen by summary.`,
	}
	k.PersistentFlags().StringVar(&dir, "dir", ".", "project work dir")
	open := func() (*kb.Library, error) {
		res := "resources"
		if c, err := projectconfig.Load(dir); err == nil {
			res = c.Resources
		} else if !errors.Is(err, projectconfig.ErrNotFound) {
			return nil, err
		}
		return kb.Open(dir, res)
	}

	var kind, title string
	var tags []string
	add := &cobra.Command{
		Use:   "add <file|dir>...",
		Short: "Copy files into resources/<kind>/ and index them (dedupe by sha256)",
		Example: `  pcbpilot kb add ~/Downloads/AMS1117.pdf --kind datasheet --tag power
  pcbpilot kb add ./papers --kind paper`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			res, err := lib.Add(args, kb.AddOptions{Kind: kind, Tags: tags, Title: title})
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"added": res})
		},
	}
	add.Flags().StringVar(&kind, "kind", "other", "kind: "+strings.Join(kb.Kinds, " | "))
	add.Flags().StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	add.Flags().StringVar(&title, "title", "", "title override (single file)")

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List documents (id, kind, status, pages, summary state)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			cat, err := lib.Load()
			if err != nil {
				return err
			}
			if listJSON {
				return writeJSON(stdout, cat)
			}
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tPAGES\tCHUNKS\tSUMMARY\tTAGS\tPATH")
			for _, d := range cat.Docs {
				sum := "-"
				if d.Summary != nil {
					sum = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n", d.ID, d.Kind, d.Status, d.Pages, d.Chunks, sum, strings.Join(d.Tags, ","), d.Path)
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, "print catalog JSON")

	var topK, perDoc int
	var sKind, sTag, sDoc string
	var docsOnly, sJSON bool
	search := &cobra.Command{
		Use:   "search <query>",
		Short: "BM25 search over page-bounded chunks; --docs screens documents first",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			res, err := lib.Search(strings.Join(args, " "), kb.SearchOptions{K: topK, Kind: sKind, Tag: sTag, Doc: sDoc, PerDoc: perDoc})
			if err != nil {
				return err
			}
			if sJSON {
				return writeJSON(stdout, res)
			}
			fmt.Fprintf(stdout, "%d docs / %d chunks searched in %d ms; terms: %s\n", res.Docs, res.Chunks, res.TookMs, strings.Join(res.Terms, " "))
			if docsOnly {
				tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "SCORE\tID\tHITS\tPAGES\tTITLE")
				for _, d := range res.ByDoc {
					fmt.Fprintf(tw, "%.2f\t%s\t%d\t%s\t%s\n", d.Score, d.DocID, d.Hits, intsCSV(d.Pages), d.Title)
				}
				return tw.Flush()
			}
			for _, h := range res.Hits {
				fmt.Fprintf(stdout, "\n[%.2f] %s  %s  (%s)\n  %s\n", h.Score, h.Cite, h.Title, h.Path, h.Snippet)
			}
			if len(res.Hits) == 0 {
				fmt.Fprintln(stdout, "no hits")
			}
			return nil
		},
	}
	sf := search.Flags()
	sf.IntVarP(&topK, "top", "k", 10, "max chunk hits")
	sf.IntVar(&perDoc, "per-doc", 3, "max hits per document")
	sf.StringVar(&sKind, "kind", "", "only this kind")
	sf.StringVar(&sTag, "tag", "", "only documents with this tag")
	sf.StringVar(&sDoc, "doc", "", "only this document id (prefix ok)")
	sf.BoolVar(&docsOnly, "docs", false, "print per-document screening table instead of chunks")
	sf.BoolVar(&sJSON, "json", false, "print JSON")

	var pages string
	var chunk int
	var showJSON bool
	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a document's metadata and summary; --pages / --chunk prints only that text (deep-read)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			cat, err := lib.Load()
			if err != nil {
				return err
			}
			d, err := cat.Find(args[0])
			if err != nil {
				return err
			}
			var sel []kb.Chunk
			if pages != "" || cmd.Flags().Changed("chunk") {
				chunks, err := lib.Chunks(d.ID)
				if err != nil {
					return fmt.Errorf("no indexed text for %s (%s): %w", d.ID, d.Status, err)
				}
				lo, hi := 0, 0
				if pages != "" {
					if lo, hi, err = parseRange(pages); err != nil {
						return err
					}
				}
				for _, c := range chunks {
					if (pages != "" && c.Page >= lo && c.Page <= hi) || (cmd.Flags().Changed("chunk") && c.N == chunk) {
						sel = append(sel, c)
					}
				}
			}
			if showJSON {
				return writeJSON(stdout, map[string]any{"doc": d, "chunks": sel})
			}
			fmt.Fprintf(stdout, "%s  %s\nkind=%s status=%s pages=%d chunks=%d extractor=%s\npath: %s\n", d.ID, d.Title, d.Kind, d.Status, d.Pages, d.Chunks, d.Extractor, d.Path)
			if d.Note != "" {
				fmt.Fprintf(stdout, "note: %s\n", d.Note)
			}
			if d.Summary != nil {
				fmt.Fprintf(stdout, "summary (%s, %s): %s\n", d.Summary.By, d.Summary.At, d.Summary.Text)
				for _, p := range d.Summary.KeyPoints {
					fmt.Fprintf(stdout, "  - %s\n", p)
				}
			} else {
				fmt.Fprintln(stdout, "summary: (empty — fill with `pcbpilot kb set-summary` after reading)")
			}
			for _, c := range sel {
				fmt.Fprintf(stdout, "\n── %s (chunk %d) ──\n%s\n", kb.Cite(d.ID, c.Page, c.N), c.N, c.Text)
			}
			return nil
		},
	}
	show.Flags().StringVar(&pages, "pages", "", "page or range to print, e.g. 3 or 3-5")
	show.Flags().IntVar(&chunk, "chunk", 0, "chunk number to print")
	show.Flags().BoolVar(&showJSON, "json", false, "print JSON")

	var sumText, sumFile, sumBy string
	var keyPoints []string
	setSummary := &cobra.Command{
		Use:   "set-summary <id>",
		Short: "Fill a document's summary slot (after deep-reading it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			text := sumText
			if sumFile != "" {
				b, err := os.ReadFile(sumFile)
				if err != nil {
					return err
				}
				text = string(b)
			}
			d, err := lib.SetSummary(args[0], kb.Summary{Text: strings.TrimSpace(text), KeyPoints: keyPoints, By: sumBy})
			if err != nil {
				return err
			}
			return writeJSON(stdout, d)
		},
	}
	setSummary.Flags().StringVar(&sumText, "text", "", "summary text")
	setSummary.Flags().StringVar(&sumFile, "file", "", "read the summary from a file")
	setSummary.Flags().StringArrayVar(&keyPoints, "point", nil, "key point (repeatable), cite pages like kb:<id>#p3")
	setSummary.Flags().StringVar(&sumBy, "by", os.Getenv("PCBPILOT_CLIENT_LABEL"), "who wrote it (agent label)")

	var addTags, rmTags []string
	tag := &cobra.Command{
		Use:   "tag <id>",
		Short: "Add or remove tags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			d, err := lib.SetTags(args[0], addTags, rmTags)
			if err != nil {
				return err
			}
			return writeJSON(stdout, d)
		},
	}
	tag.Flags().StringArrayVar(&addTags, "add", nil, "tag to add")
	tag.Flags().StringArrayVar(&rmTags, "remove", nil, "tag to remove")

	status := &cobra.Command{
		Use:   "summarize-status",
		Short: "Which documents still need a summary (the agent's reading queue)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			cat, err := lib.Load()
			if err != nil {
				return err
			}
			var pending, done, unindexed []map[string]any
			for _, d := range cat.Docs {
				row := map[string]any{"id": d.ID, "title": d.Title, "name": d.Name, "kind": d.Kind, "pages": d.Pages, "status": d.Status}
				switch {
				case d.Status != "indexed":
					row["note"] = d.Note
					unindexed = append(unindexed, row)
				case d.Summary == nil:
					pending = append(pending, row)
				default:
					done = append(done, row)
				}
			}
			return writeJSON(stdout, map[string]any{"docs": len(cat.Docs), "summarized": len(done), "needsSummary": pending, "notIndexed": unindexed})
		},
	}

	reindex := &cobra.Command{
		Use:   "reindex",
		Short: "Re-extract and re-index every document (after upgrading pcbpilot or installing pdftotext)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			lib, err := open()
			if err != nil {
				return err
			}
			cat, err := lib.Reindex()
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "reindexed %d documents\n", len(cat.Docs))
			return nil
		},
	}
	k.AddCommand(add, list, search, show, setSummary, tag, status, reindex)
	return k
}

func parseRange(s string) (int, int, error) {
	a, b, ok := strings.Cut(s, "-")
	lo, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || lo < 1 {
		return 0, 0, fmt.Errorf("bad page range %q", s)
	}
	if !ok {
		return lo, lo, nil
	}
	hi, err := strconv.Atoi(strings.TrimSpace(b))
	if err != nil || hi < lo {
		return 0, 0, fmt.Errorf("bad page range %q", s)
	}
	return lo, hi, nil
}

func intsCSV(v []int) string {
	var s []string
	for _, i := range v {
		s = append(s, strconv.Itoa(i))
	}
	return strings.Join(s, ",")
}

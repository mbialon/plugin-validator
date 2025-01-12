package brokenlinks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/grafana/plugin-validator/pkg/analysis"
	"github.com/grafana/plugin-validator/pkg/analysis/passes/metadata"
	"github.com/grafana/plugin-validator/pkg/analysis/passes/readme"
)

var mdLinks = regexp.MustCompile(`\[.+?\]\((.+?)\)`)

var Analyzer = &analysis.Analyzer{
	Name:     "brokenlinks",
	Requires: []*analysis.Analyzer{metadata.Analyzer, readme.Analyzer},
	Run:      run,
}

type contextURL struct {
	context string
	url     string
}

func run(pass *analysis.Pass) (interface{}, error) {
	metadataBody := pass.ResultOf[metadata.Analyzer].([]byte)
	readme := pass.ResultOf[readme.Analyzer].([]byte)

	var urls []contextURL

	var data metadata.Metadata
	if err := json.Unmarshal(metadataBody, &data); err != nil {
		return nil, err
	}

	if data.Info.Author.URL != "" {
		urls = append(urls, contextURL{
			context: "plugin.json",
			url:     data.Info.Author.URL,
		})
	}

	for _, link := range data.Info.Links {
		urls = append(urls, contextURL{
			context: "plugin.json",
			url:     link.URL,
		})
	}

	matches := mdLinks.FindAllSubmatch(readme, -1)

	for _, m := range matches {
		path := string(m[1])

		if strings.HasPrefix(path, "#") {
			// Named anchors are allowed, but not checked.
			continue
		}

		// Strip optional alt text for images, e.g. ![image](./path/to/image "alt text").
		fields := strings.Fields(path)
		if len(fields) > 0 {
			path = fields[0]
		}

		if strings.HasPrefix(path, "mailto:") {
			continue
		}

		if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
			urls = append(urls, contextURL{
				context: "README.md",
				url:     path,
			})
		} else {
			pass.Report(analysis.Diagnostic{
				Severity: analysis.Error,
				Message:  fmt.Sprintf("convert relative link to absolute: %s", path),
			})
		}
	}

	type urlstatus struct {
		url     string
		status  string
		context string
	}

	brokenCh := make(chan urlstatus)

	var wg sync.WaitGroup
	wg.Add(len(urls))

	for _, u := range urls {
		go func(url contextURL) {
			defer wg.Done()

			req, err := http.NewRequest("GET", url.url, nil)
			if err != nil {
				brokenCh <- urlstatus{url: url.url, status: err.Error(), context: url.context}
				return
			}
			req.Header.Add("User-Agent", "Mozilla/5.0 (compatible; GrafanaPluginValidatorBot; +https://github.com/grafana/plugin-validator)")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				brokenCh <- urlstatus{url: url.url, status: err.Error(), context: url.context}
				return
			}

			if resp.StatusCode != http.StatusOK {
				brokenCh <- urlstatus{url: url.url, status: resp.Status, context: url.context}
			}
		}(u)
	}

	go func() {
		wg.Wait()
		close(brokenCh)
	}()

	for link := range brokenCh {
		pass.Report(analysis.Diagnostic{
			Severity: analysis.Error,
			Message:  fmt.Sprintf("broken link: %s (%s)", link.url, link.status),
			Context:  link.context,
		})
	}

	return nil, nil
}

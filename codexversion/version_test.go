package codexversion

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStableVersions(t *testing.T) {
	if !Valid(strings.TrimSpace(embeddedVersion)) {
		t.Fatal("tracked VERSION must contain a stable Codex version")
	}
	for _, version := range []string{"0.161.0", "1.0.0", "0.1.2"} {
		if !Valid(version) {
			t.Errorf("valid version rejected: %q", version)
		}
	}
	for _, version := range []string{"", "dev", "0.0.0", "0.161", "0.161.0.1", "v0.161.0", "rust-v0.161.0", "0.161.0-alpha.1", "0.161.0+dev", " 0.161.0", "0.161.0\n", "+0.161.0", "0.0161.0", "0.161.-1", "0.18446744073709551616.0"} {
		if Valid(version) {
			t.Errorf("invalid version accepted: %q", version)
		}
	}
	for _, tc := range []struct{ current, candidate, want string }{
		{"0.9.0", "0.10.0", "0.10.0"}, {"0.161.0", "0.9.0", "0.161.0"},
		{"0.161.0", "1.0.0", "1.0.0"}, {"1.0.0", "1.0.1", "1.0.1"},
		{"0.161.0", "bad", "0.161.0"}, {"bad", "0.161.0", "0.161.0"},
	} {
		if got := Newer(tc.current, tc.candidate); got != tc.want {
			t.Errorf("Newer(%q,%q) = %q", tc.current, tc.candidate, got)
		}
	}
}

func TestBundledBuildFallback(t *testing.T) {
	prev := BuildVersion
	t.Cleanup(func() { BuildVersion = prev })
	for _, version := range []string{"", "dev", "0.161.0-alpha.1", "\r\n"} {
		BuildVersion = version
		if got := Bundled(); got != strings.TrimSpace(embeddedVersion) {
			t.Fatalf("fallback = %q", got)
		}
	}
	BuildVersion = "1.2.3"
	if got := Bundled(); got != "1.2.3" {
		t.Fatalf("build override = %q", got)
	}
}

func TestLatestHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, npm, github, want string
		status                  int
	}{
		{"npm", `{"name":"@openai/codex","version":"0.161.0"}`, "", "0.161.0", 200},
		{"rate limit", "", `{"tag_name":"rust-v0.162.0"}`, "0.162.0", 429},
		{"npm invalid JSON", `{`, `{"tag_name":"rust-v0.162.0"}`, "0.162.0", 200},
		{"npm wrong package", `{"name":"other","version":"1.0.0"}`, `{"tag_name":"rust-v0.162.0"}`, "0.162.0", 200},
		{"npm prerelease", `{"name":"@openai/codex","version":"0.162.0-alpha.1"}`, `{"tag_name":"rust-v0.161.0"}`, "0.161.0", 200},
		{"all invalid", `{}`, `{}`, "", 200},
		{"github prerelease", `{}`, `{"tag_name":"rust-v0.162.0-alpha.1","prerelease":true}`, "", 200},
		{"github flagged prerelease", `{}`, `{"tag_name":"rust-v0.162.0","prerelease":true}`, "", 200},
		{"github draft", `{}`, `{"tag_name":"rust-v0.162.0","draft":true}`, "", 200},
		{"github wrong tag", `{}`, `{"tag_name":"v0.162.0"}`, "", 200},
		{"github invalid JSON", `{}`, `{`, "", 200},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), `{}`, "", 200},
		{"trailing garbage", `{"name":"@openai/codex","version":"0.161.0"} {}`, `{}`, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var githubCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") == "" {
					t.Error("invalid lookup request")
				}
				if r.URL.Path == "/npm" {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.npm)
				} else {
					githubCalls.Add(1)
					fmt.Fprint(w, tc.github)
				}
			}))
			defer srv.Close()
			got, err := (Fetcher{Client: srv.Client(), NPMURL: srv.URL + "/npm", GitHubURL: srv.URL + "/github"}).Latest(context.Background())
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v", got, err)
			}
			if tc.name == "npm" && githubCalls.Load() != 0 {
				t.Fatal("queried GitHub despite npm success")
			}
		})
	}
}

func TestLatestTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                string
		cancel, githubHangs bool
	}{
		{"first source hangs", false, false}, {"both sources hang", false, true}, {"cancelled", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/npm" || tc.githubHangs {
					<-r.Context().Done()
					return
				}
				fmt.Fprint(w, `{"tag_name":"rust-v0.161.0"}`)
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			start := time.Now()
			got, err := (Fetcher{Client: srv.Client(), NPMURL: srv.URL + "/npm", GitHubURL: srv.URL + "/github", Timeout: 100 * time.Millisecond}).Latest(ctx)
			if time.Since(start) > time.Second {
				t.Fatal("lookup exceeded total timeout")
			}
			if tc.cancel || tc.githubHangs {
				if err == nil {
					t.Fatal("expected failure")
				}
			} else if err != nil || got != "0.161.0" {
				t.Fatalf("fallback source = %q, %v", got, err)
			}
		})
	}
}

func TestLatestOffline(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()
	if _, err := (Fetcher{NPMURL: endpoint, GitHubURL: endpoint}).Latest(context.Background()); err == nil {
		t.Fatal("offline lookup succeeded")
	}
}

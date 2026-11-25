package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

func main() {
	tok, _ := os.ReadFile(os.Getenv("HOME") + "/.config/gh-hud/hub-token")
	c := api.DialTCP(api.Discovery{URL: "http://192.168.1.59:7377", Token: strings.TrimSpace(string(tok))})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if len(os.Args) == 5 && os.Args[1] == "act" {
		out, err := c.CheckoutAction(ctx, os.Args[2], os.Args[3], dispatch.CheckoutAction(os.Args[4]))
		fmt.Println(out, err)
		return
	}
	s, err := c.Fetch(ctx)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "json" {
		json.NewEncoder(os.Stdout).Encode(s.Workspaces)
		return
	}
	for _, h := range s.Workspaces {
		m := h.Machine
		if m == "" {
			m = "local"
		}
		notes := ""
		if r := h.LastReindex; r != nil && r.Failed() {
			b, _ := json.Marshal(r)
			notes = " REINDEXFAIL " + string(b)
		}
		if h.Problem() == "" && h.LocalOnlyCommits() == 0 && h.Stashes == 0 && notes == "" {
			continue
		}
		fmt.Printf("%s\t%s\t%s\tproblem=%q localonly=%v stash=%d learn=%d\n", m, h.Dir, h.Branch, h.Problem(), h.LocalOnly, h.Stashes, h.Learnings)
		for _, d := range h.Dirty {
			fmt.Println("    dirty:", d)
		}
		if notes != "" {
			fmt.Println("   ", notes)
		}
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/sdk"
	"github.com/spf13/cobra"
)

// newRecipeCmd 组装 `mow recipe list|run` 子命令。
func newRecipeCmd(h *appHolder) *cobra.Command {
	c := &cobra.Command{
		Use:   "recipe",
		Short: "Manage and run built-in recipes",
	}
	c.AddCommand(
		newRecipeListCmd(h),
		newRecipeRunCmd(h),
	)
	return c
}

// -----------------------------------------------------------------------------
// mow recipe list
// -----------------------------------------------------------------------------

func newRecipeListCmd(h *appHolder) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List available recipes",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			items := app.Recipes.List()
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(items)
			}
			if len(items) == 0 {
				fmt.Println("(no recipes)")
				return nil
			}
			for _, rp := range items {
				fmt.Printf("%-16s  %s\n", rp.ID, rp.Description)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return c
}

// -----------------------------------------------------------------------------
// mow recipe run <id> --target=<targetID>
// -----------------------------------------------------------------------------

func newRecipeRunCmd(h *appHolder) *cobra.Command {
	var (
		target string
		asJSON bool
	)
	c := &cobra.Command{
		Use:   "run <recipe-id>",
		Short: "Execute a recipe against a target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			rp, ok := app.Recipes.Get(args[0])
			if !ok {
				return fmt.Errorf("recipe not found: %s", args[0])
			}
			if target == "" {
				return fmt.Errorf("--target is required")
			}
			// 提前把 recipe 用到的插件按需 enable。
			ctx := cmd.Context()
			for _, id := range distinctPlugins(rp) {
				if err := app.ensurePluginEnabled(ctx, id); err != nil {
					return err
				}
			}
			res, runErr := app.Runner.Run(ctx, rp, recipe.RunOptions{
				TargetID: target,
				Caller:   sdk.Caller{Type: sdk.CallerCLI, User: currentUser()},
			})
			if asJSON {
				_ = json.NewEncoder(os.Stdout).Encode(res)
			} else {
				printRecipeResult(res)
			}
			if runErr != nil {
				return runErr
			}
			return nil
		},
	}
	c.Flags().StringVar(&target, "target", "", "target ID (required)")
	c.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return c
}

func distinctPlugins(rp *recipe.Recipe) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range rp.Steps {
		if _, ok := seen[s.Plugin]; ok {
			continue
		}
		seen[s.Plugin] = struct{}{}
		out = append(out, s.Plugin)
	}
	return out
}

func printRecipeResult(res *recipe.Result) {
	if res == nil {
		return
	}
	status := "ok"
	if !res.OK {
		status = "FAILED"
	}
	fmt.Printf("recipe=%s status=%s duration=%s\n", res.RecipeID, status, res.Duration.Round(time.Millisecond))
	for _, s := range res.Steps {
		if s.OK {
			fmt.Printf("  ✓ %-10s %s.%s  (%s)\n", s.StepID, s.Plugin, s.Command, s.Duration.Round(time.Millisecond))
			if len(s.Data) > 0 {
				fmt.Printf("    %s\n", strings.TrimSpace(string(s.Data)))
			}
		} else {
			fmt.Printf("  ✗ %-10s %s.%s  code=%s  err=%s\n", s.StepID, s.Plugin, s.Command, s.ErrorCode, s.ErrorMsg)
		}
	}
}

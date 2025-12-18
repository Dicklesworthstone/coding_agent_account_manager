package cmd

import "testing"

func TestReloadCommandStructure(t *testing.T) {
	if reloadCmd.Use != "reload" {
		t.Fatalf("Use=%q, want %q", reloadCmd.Use, "reload")
	}

	flag := reloadCmd.Flags().Lookup("pid-file")
	if flag == nil {
		t.Fatal("expected --pid-file flag")
	}

	if err := reloadCmd.Args(reloadCmd, []string{}); err != nil {
		t.Fatalf("expected no args allowed: %v", err)
	}
}

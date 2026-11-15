package kirorelease

import (
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestSpawnEnv_TurnsSelfUpdateOffWithoutTouchingTheInput(t *testing.T) {
	in := make([]string, 1, 4)
	in[0] = noAutoUpdateVar + "=0"
	got := SpawnEnv(in)
	if want := []string{noAutoUpdateVar + "=0", noAutoUpdateVar + "=1"}; !slices.Equal(got, want) {
		t.Errorf("SpawnEnv = %q, want %q: the last value is the one a child sees", got, want)
	}
	if spare := in[:2]; spare[1] != "" {
		t.Errorf("SpawnEnv wrote %q into the caller's spare capacity", spare[1])
	}
	if got := SpawnEnv(nil); !slices.Equal(got, []string{noAutoUpdateVar + "=1"}) {
		t.Errorf("SpawnEnv(nil) = %q, want only the switch", got)
	}
}

func TestSetCommandEnv_AddsPWDForADirOnly(t *testing.T) {
	in := []string{"PWD=/caller"}
	cmd := exec.Command("true")
	SetCommandEnv(cmd, in)
	if want := SpawnEnv(in); !slices.Equal(cmd.Env, want) {
		t.Errorf("no Dir: Env = %q, want %q", cmd.Env, want)
	}
	abs, err := filepath.Abs("sub")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/work/sub", "sub"} {
		want := dir
		if !filepath.IsAbs(dir) {
			want = abs
		}
		cmd := exec.Command("true")
		cmd.Dir = dir
		SetCommandEnv(cmd, in)
		if wantEnv := append(SpawnEnv(in), "PWD="+want); !slices.Equal(cmd.Env, wantEnv) {
			t.Errorf("Dir %q: Env = %q, want %q", dir, cmd.Env, wantEnv)
		}
		if cmd.Err != nil {
			t.Errorf("Dir %q: Err = %v", dir, cmd.Err)
		}
	}
}

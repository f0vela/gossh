package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundtripAndDefaults(t *testing.T) {
	s := WithPath(filepath.Join(t.TempDir(), "settings.json"))
	got, err := s.Load()
	if err != nil || got != (Settings{}) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	want := Settings{RequireOnLaunch: true, RequireForCredentials: true}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || got != want {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	if st, _ := os.Stat(s.path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestCorruptFileIsAnErrorNotAReset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte("{not json"), 0o600)
	if _, err := WithPath(p).Load(); err == nil {
		t.Fatal("corrupt settings must not silently load as defaults")
	}
}

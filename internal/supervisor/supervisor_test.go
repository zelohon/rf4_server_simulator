package supervisor

import "testing"

func TestNewSupervisesOnlyGameServices(t *testing.T) {
	s := New(t.TempDir(), "config.yaml", "")
	status := s.Status()
	if len(status) != 2 {
		t.Fatalf("process count=%d, want 2", len(status))
	}
	want := []string{"gameserver", "gateway"}
	for i, name := range want {
		if status[i].Name != name {
			t.Fatalf("status[%d]=%q, want %q", i, status[i].Name, name)
		}
	}
}

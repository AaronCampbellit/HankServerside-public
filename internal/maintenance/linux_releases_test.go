package maintenance

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrunableLinuxVersionsRetainsActivePreviousAndProtected(t *testing.T) {
	versions := []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", "0.6.0", "invalid"}
	protected := map[string]struct{}{"0.1.0": {}}
	if got, want := PrunableLinuxVersions(versions, "0.6.0", 3, protected), []string{"0.2.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prunable = %v, want %v", got, want)
	}
}

func TestRemoveLinuxVersionDirectoriesRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "0.1.0")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLinuxVersionDirectories(root, []string{"0.1.0"}); err == nil {
		t.Fatal("symlink release directory was removed")
	}
}

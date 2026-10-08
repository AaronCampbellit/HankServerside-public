package smb2

import (
	"errors"
	"os"
	"testing"
)

// These raw operations remain available after dropping the unused parsed ACL
// wrappers. Invalid input must be rejected before opening an SMB connection.
func TestHankRawSecurityInfoRejectsInvalidRequests(t *testing.T) {
	var share Share
	operations := []struct {
		name string
		run  func(string, SecurityInformationRequestFlags) error
	}{
		{"read", func(path string, flags SecurityInformationRequestFlags) error {
			_, err := share.SecurityInfoRaw(path, flags)
			return err
		}},
		{"write", func(path string, flags SecurityInformationRequestFlags) error {
			return share.SetSecurityInfoRaw(path, flags, nil)
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, path := range []string{`\absolute`, "/absolute"} {
				if err := operation.run(path, OwnerSecurityInformation); err == nil {
					t.Fatalf("raw security request accepted invalid path %q", path)
				}
			}
			if err := operation.run("file.txt", 0); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("zero information flags: got %v, want os.ErrInvalid", err)
			}
		})
	}
}

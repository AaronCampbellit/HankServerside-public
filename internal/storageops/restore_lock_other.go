//go:build !unix

package storageops

import "errors"

func acquireRestoreLock(string) (func(), error) {
	return nil, errors.New("paired restore requires a Unix database operations host")
}
func attachmentGroup(string) (int, error) {
	return 0, errors.New("paired restore requires Unix group permissions")
}

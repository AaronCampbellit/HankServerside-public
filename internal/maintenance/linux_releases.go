package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var linuxVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func PrunableLinuxVersions(versions []string, active string, retained int, protected map[string]struct{}) []string {
	if retained < 0 {
		retained = 0
	}
	valid := make([]string, 0, len(versions))
	for _, version := range versions {
		if linuxVersionPattern.MatchString(version) {
			valid = append(valid, version)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return compareLinuxVersions(valid[i], valid[j]) > 0 })
	keep := make(map[string]struct{}, retained+len(protected)+1)
	keep[active] = struct{}{}
	for value := range protected {
		keep[value] = struct{}{}
	}
	previous := 0
	for _, version := range valid {
		if version == active {
			continue
		}
		if previous < retained {
			keep[version] = struct{}{}
			previous++
		}
	}
	var result []string
	for _, version := range valid {
		if _, ok := keep[version]; !ok {
			result = append(result, version)
		}
	}
	return result
}

func RemoveLinuxVersionDirectories(root string, versions []string) error {
	root, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || root == "/" || root == "." {
		return fmt.Errorf("invalid Linux release root")
	}
	for _, version := range versions {
		if !linuxVersionPattern.MatchString(version) {
			return fmt.Errorf("invalid Linux release version %q", version)
		}
		path := filepath.Join(root, version)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing non-directory Linux release path %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func compareLinuxVersions(left, right string) int {
	lp, rp := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < 3; index++ {
		lv, _ := strconv.ParseUint(lp[index], 10, 64)
		rv, _ := strconv.ParseUint(rp[index], 10, 64)
		if lv < rv {
			return -1
		}
		if lv > rv {
			return 1
		}
	}
	return 0
}

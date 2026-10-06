package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type runtimeStorage struct {
	dataDir string
	railway bool
}

var railwayRuntimeIDs = []string{
	"RAILWAY_PROJECT_ID", "RAILWAY_ENVIRONMENT_ID", "RAILWAY_SERVICE_ID", "RAILWAY_DEPLOYMENT_ID",
}

// resolveRuntimeStorage is read-only: startup and the container entrypoint must
// approve the same location before creating state or changing directory ownership.
func resolveRuntimeStorage(getenv func(string) string, readFile func(string) ([]byte, error)) (runtimeStorage, error) {
	var result runtimeStorage
	result.railway = strings.TrimSpace(getenv("RAILWAY_VOLUME_MOUNT_PATH")) != ""
	for _, key := range railwayRuntimeIDs {
		result.railway = result.railway || strings.TrimSpace(getenv(key)) != ""
	}
	dataDir := strings.TrimSpace(getenv("DATA_DIR"))
	if !result.railway {
		if dataDir == "" {
			dataDir = "data"
		}
		// Existing local records may contain relative media paths. Preserve the
		// configured path so their authorization and deletion checks still work.
		result.dataDir = dataDir
		return result, nil
	}

	const remediation = "; attach a persistent volume to this Railway service at /data and set DATA_DIR=/data"
	volume := strings.TrimSpace(getenv("RAILWAY_VOLUME_MOUNT_PATH"))
	if !filepath.IsAbs(volume) || filepath.Clean(volume) == filepath.VolumeName(volume)+string(filepath.Separator) {
		return result, errors.New("Railway requires an absolute, non-root RAILWAY_VOLUME_MOUNT_PATH" + remediation)
	}
	volume = filepath.Clean(volume)
	if dataDir == "" {
		dataDir = volume
	}
	if !filepath.IsAbs(dataDir) {
		return result, errors.New("Railway DATA_DIR must be absolute" + remediation)
	}
	dataDir = filepath.Clean(dataDir)
	if !pathWithin(volume, dataDir) {
		return result, errors.New("Railway DATA_DIR must be inside the persistent volume" + remediation)
	}
	mountInfo, err := readFile("/proc/self/mountinfo")
	if err != nil || !hasMountPoint(string(mountInfo), volume) {
		return result, errors.New("Railway persistent volume is not mounted at RAILWAY_VOLUME_MOUNT_PATH" + remediation)
	}
	physicalVolume, err := filepath.EvalSymlinks(volume)
	if err != nil || !sameStoragePath(physicalVolume, volume) {
		return result, errors.New("Railway volume mount path must be an existing directory without symlink aliases" + remediation)
	}
	volumeInfo, err := os.Stat(volume)
	if err != nil || !volumeInfo.IsDir() {
		return result, errors.New("Railway persistent volume path is not a directory" + remediation)
	}
	physicalData, err := resolveExistingAncestor(dataDir)
	if err != nil || !pathWithin(physicalVolume, physicalData) {
		return result, errors.New("Railway DATA_DIR resolves outside the persistent volume or has an inaccessible ancestor" + remediation)
	}
	for _, child := range []string{"app.db", "secret.key", "videos", "projects"} {
		path := filepath.Join(physicalData, child)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, errors.New("Railway persistent storage entry is inaccessible" + remediation)
		}
		physicalChild, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithin(physicalVolume, physicalChild) {
			return result, errors.New("Railway database, encryption key, and media directories must resolve inside the persistent volume" + remediation)
		}
	}
	// Return the physical path so the privileged entrypoint and service use the
	// same target, including when the final subdirectories do not yet exist.
	result.dataDir = physicalData
	return result, nil
}

func pathWithin(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func sameStoragePath(first, second string) bool {
	relative, err := filepath.Rel(first, second)
	return err == nil && relative == "."
}

// Resolve the existing ancestor first, then append absent children. Lstat
// distinguishes dangling symlinks from missing directories and rejects them.
func resolveExistingAncestor(path string) (string, error) {
	ancestor := path
	var children []string
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			physical, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				resolvedInfo, err := os.Stat(physical)
				if err != nil || !resolvedInfo.IsDir() {
					return "", errors.New("data path ancestor is not a directory")
				}
			}
			for i := len(children) - 1; i >= 0; i-- {
				physical = filepath.Join(physical, children[i])
			}
			return physical, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("data path has no existing ancestor")
		}
		children = append(children, filepath.Base(ancestor))
		ancestor = parent
	}
}

func hasMountPoint(mountInfo, expected string) bool {
	for _, line := range strings.Split(mountInfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !strings.Contains(line, " - ") {
			continue
		}
		// Linux mountinfo escapes these four characters as octal sequences.
		mountPoint := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(fields[4])
		if mountPoint == expected {
			return true
		}
	}
	return false
}

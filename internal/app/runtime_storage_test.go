package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticMountInfo(path string) []byte {
	escaped := strings.NewReplacer(`\`, `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`).Replace(path)
	return []byte("42 29 8:1 / " + escaped + " rw,relatime - ext4 /dev/synthetic rw\n")
}

func resolveStorageFixture(env map[string]string, mountInfo []byte) (runtimeStorage, error) {
	return resolveRuntimeStorage(func(key string) string { return env[key] }, func(path string) ([]byte, error) {
		if path != "/proc/self/mountinfo" {
			return nil, errors.New("unexpected file read")
		}
		return mountInfo, nil
	})
}

func TestRuntimeStorageLocalDefaults(t *testing.T) {
	for _, configured := range []string{"", "  ", " local-data "} {
		got, err := resolveRuntimeStorage(func(key string) string {
			if key == "DATA_DIR" {
				return configured
			}
			return ""
		}, func(string) ([]byte, error) { t.Fatal("local mode read mountinfo"); return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		path := strings.TrimSpace(configured)
		if path == "" {
			path = "data"
		}
		want := path
		if got.dataDir != want || got.railway {
			t.Fatalf("unexpected storage resolution: %+v", got)
		}
	}
}

func TestRuntimeStorageRailwayValidation(t *testing.T) {
	root := t.TempDir()
	volume := filepath.Join(root, "mounted volume")
	if err := os.Mkdir(volume, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	tests := []struct {
		name, marker, mount, data string
		info                      []byte
		wantError                 bool
	}{
		{"mounted default", "RAILWAY_PROJECT_ID", volume, "", syntheticMountInfo(volume), false},
		{"trimmed mounted path", "RAILWAY_ENVIRONMENT_ID", "  " + volume + "  ", "  " + volume + "  ", syntheticMountInfo(volume), false},
		{"new nested directory", "RAILWAY_SERVICE_ID", volume, filepath.Join(volume, "new", "nested"), syntheticMountInfo(volume), false},
		{"deployment only", "RAILWAY_DEPLOYMENT_ID", volume, volume, syntheticMountInfo(volume), false},
		{"mount marker only", "", volume, volume, syntheticMountInfo(volume), false},
		{"missing mount metadata", "RAILWAY_PROJECT_ID", "", volume, syntheticMountInfo(volume), true},
		{"relative mount metadata", "RAILWAY_PROJECT_ID", "data", volume, syntheticMountInfo(volume), true},
		{"root mount", "RAILWAY_PROJECT_ID", filepath.VolumeName(volume) + string(filepath.Separator), volume, syntheticMountInfo(volume), true},
		{"relative data directory", "RAILWAY_PROJECT_ID", volume, "data", syntheticMountInfo(volume), true},
		{"outside directory", "RAILWAY_PROJECT_ID", volume, outside, syntheticMountInfo(volume), true},
		{"prefix is not containment", "RAILWAY_PROJECT_ID", volume, volume + "-outside", syntheticMountInfo(volume), true},
		{"traversal", "RAILWAY_PROJECT_ID", volume, volume + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside", syntheticMountInfo(volume), true},
		{"directory without mount", "RAILWAY_PROJECT_ID", volume, volume, nil, true},
		{"different mount", "RAILWAY_PROJECT_ID", volume, volume, syntheticMountInfo(root), true},
		{"malformed mountinfo", "RAILWAY_PROJECT_ID", volume, volume, []byte("42 29 8:1 / " + volume), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"RAILWAY_VOLUME_MOUNT_PATH": tc.mount, "DATA_DIR": tc.data}
			if tc.marker != "" {
				env[tc.marker] = "synthetic-id"
			}
			got, err := resolveStorageFixture(env, tc.info)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
			if err == nil {
				want := strings.TrimSpace(tc.data)
				if want == "" {
					want = volume
				}
				if !sameStoragePath(got.dataDir, filepath.Clean(want)) || !got.railway {
					t.Fatalf("unexpected resolved storage: %+v", got)
				}
				if strings.Contains(tc.name, "new nested") {
					if _, err := os.Stat(got.dataDir); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("resolver created data directory")
					}
				}
			}
		})
	}
	_, err := resolveRuntimeStorage(func(key string) string {
		return map[string]string{"RAILWAY_PROJECT_ID": "synthetic", "RAILWAY_VOLUME_MOUNT_PATH": volume}[key]
	}, func(string) ([]byte, error) { return nil, errors.New("unreadable") })
	if err == nil {
		t.Fatal("unreadable mountinfo was accepted")
	}
}

func TestRuntimeStorageSymlinkContainment(t *testing.T) {
	root := t.TempDir()
	volume, outside := filepath.Join(root, "volume"), filepath.Join(root, "outside")
	for _, path := range []string{volume, outside} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(volume, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	env := map[string]string{"RAILWAY_PROJECT_ID": "synthetic", "RAILWAY_VOLUME_MOUNT_PATH": volume}
	for _, path := range []string{link, filepath.Join(link, "new", "nested")} {
		env["DATA_DIR"] = path
		if _, err := resolveStorageFixture(env, syntheticMountInfo(volume)); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	inside := filepath.Join(volume, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(volume, "inside-link")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Fatal(err)
	}
	env["DATA_DIR"] = filepath.Join(insideLink, "new")
	got, err := resolveStorageFixture(env, syntheticMountInfo(volume))
	if err != nil || got.dataDir != filepath.Join(inside, "new") {
		t.Fatalf("contained symlink: %+v %v", got, err)
	}
	broken := filepath.Join(volume, "broken")
	if err := os.Symlink(filepath.Join(outside, "missing"), broken); err != nil {
		t.Fatal(err)
	}
	env["DATA_DIR"] = filepath.Join(broken, "new")
	if _, err := resolveStorageFixture(env, syntheticMountInfo(volume)); err == nil {
		t.Fatal("dangling symlink accepted")
	}
	for _, child := range []string{"app.db", "secret.key", "videos", "projects"} {
		t.Run(child, func(t *testing.T) {
			path := filepath.Join(volume, child)
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			env["DATA_DIR"] = volume
			if _, err := resolveStorageFixture(env, syntheticMountInfo(volume)); err == nil {
				t.Fatal("storage child escape accepted")
			}
		})
	}
	alias := filepath.Join(root, "volume-alias")
	if err := os.Symlink(volume, alias); err != nil {
		t.Fatal(err)
	}
	env["RAILWAY_VOLUME_MOUNT_PATH"], env["DATA_DIR"] = alias, alias
	if _, err := resolveStorageFixture(env, syntheticMountInfo(alias)); err == nil {
		t.Fatal("aliased mount accepted")
	}
}

func TestRuntimeStorageMountInfoEscapes(t *testing.T) {
	for _, path := range []string{"/data", "/mounted volume", "/tab\tvolume", "/line\nvolume", `/slash\040volume`} {
		if !hasMountPoint(string(syntheticMountInfo(path)), path) {
			t.Fatalf("escaped path %q not decoded", path)
		}
	}
}

func TestRuntimeStorageCLIHelper(t *testing.T) {
	if os.Getenv("FRAMEVAULT_STORAGE_CLI_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"video-automation"}, strings.Split(os.Getenv("FRAMEVAULT_STORAGE_CLI_ARGS"), " ")...)
	Run()
	os.Exit(0)
}

func TestRuntimeStorageCLIReadOnly(t *testing.T) {
	for _, args := range []string{"storage-path", "unknown", "storage-path extra", "recovery-code extra"} {
		t.Run(args, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must-not-be-created")
			cmd := storageCLICommand(args, path)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if args == "storage-path" {
				if err != nil || !sameStoragePath(strings.TrimSuffix(stdout.String(), "\n"), path) || !strings.HasSuffix(stdout.String(), "\n") || stderr.Len() != 0 {
					t.Fatalf("unexpected storage-path output or exit: %q %q %v", stdout.String(), stderr.String(), err)
				}
			} else if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
				t.Fatal("invalid arguments were not rejected cleanly")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("CLI created storage before approval")
			}
		})
	}
}

func storageCLICommand(args, dataDir string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeStorageCLIHelper$")
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		if key == "DATA_DIR" || strings.HasPrefix(key, "RAILWAY_") || strings.HasPrefix(key, "FRAMEVAULT_STORAGE_CLI_") {
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "FRAMEVAULT_STORAGE_CLI_HELPER=1", "FRAMEVAULT_STORAGE_CLI_ARGS="+args, "DATA_DIR="+dataDir)
	return cmd
}

func TestRuntimeStorageCLIRejectsRailwayBeforeWrites(t *testing.T) {
	for _, args := range []string{"storage-path", "recovery-code"} {
		t.Run(args, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must-not-be-created")
			cmd := storageCLICommand(args, path)
			cmd.Env = append(cmd.Env, "RAILWAY_PROJECT_ID=synthetic-project")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "attach a persistent volume") {
				t.Fatal("Railway CLI without a volume was not rejected cleanly")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected Railway CLI created storage")
			}
		})
	}
}

func TestRuntimeStorageCLIResolvesPhysicalAncestor(t *testing.T) {
	root := t.TempDir()
	target, alias := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cmd := storageCLICommand("storage-path", filepath.Join(alias, "new", "nested"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || !sameStoragePath(strings.TrimSuffix(stdout.String(), "\n"), filepath.Join(target, "new", "nested")) || stderr.Len() != 0 {
		t.Fatalf("physical CLI path was not resolved: %q %q %v", stdout.String(), stderr.String(), err)
	}
	if _, err := os.Stat(filepath.Join(target, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CLI created resolved directory")
	}
}

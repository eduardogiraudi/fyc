package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// extractBinary pulls the fuckyourcli executable out of the release archive
// (tar.gz everywhere, zip on Windows).
func extractBinary(archive []byte, goos string) ([]byte, error) {
	want := "fuckyourcli"
	if goos == "windows" {
		want = "fuckyourcli.exe"
		return extractZip(archive, want)
	}
	return extractTarGz(archive, want)
}

func extractTarGz(data []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cannot read the archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read the archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != want {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, maxBinarySize))
	}
	return nil, fmt.Errorf("the archive does not contain %s", want)
}

func extractZip(data []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("cannot read the archive: %w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", want, err)
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxBinarySize))
	}
	return nil, fmt.Errorf("the archive does not contain %s", want)
}

// currentBinaryPath is the resolved path of the running executable: the update
// must replace that file, not a symlink pointing at it.
func currentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// stageBinary writes data to a fresh temp file (dir == "" means the system temp
// directory) and marks it executable. The caller owns the returned path.
func stageBinary(dir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".fyc-update-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", fmt.Errorf("cannot write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", fmt.Errorf("cannot flush %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("cannot close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o755); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// replaceBinary swaps the file at path with data.
//
// The payload is staged in a temp file first and only then renamed, so an
// interrupted update never leaves a truncated binary behind. When the install
// directory is not writable (a system-wide installation such as /usr/local/bin)
// the final move is delegated to sudo: the download and the checksum already
// happened as the current user, so no secret is handed to sudo.
//
// On Windows a running executable cannot be overwritten (only renamed): the old
// file is moved aside and removed on a best-effort basis, because the running
// process still holds it open.
func replaceBinary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpName, err := stageBinary(dir, data)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) && runtime.GOOS != "windows" {
			return stageViaSudo(path, data)
		}
		return fmt.Errorf("cannot write to %s: %w (re-run as root to replace the binary)", dir, err)
	}
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if runtime.GOOS != "windows" {
		if err := os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("cannot replace %s: %w", path, err)
		}
		return nil
	}

	old := path + ".old"
	_ = os.Remove(old)
	if err := os.Rename(path, old); err != nil {
		return fmt.Errorf("cannot move aside %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Rename(old, path) // put the previous binary back
		return fmt.Errorf("cannot replace %s: %w", path, err)
	}
	_ = os.Remove(old)
	return nil
}

// stageViaSudo installs a staged binary into a root-owned directory with a
// single sudo call. The file is copied to a temp name *inside the destination
// directory* and then renamed, because overwriting a running executable in
// place fails with ETXTBSY on Linux.
func stageViaSudo(path string, data []byte) error {
	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("cannot write to %s and sudo is not available: re-run as root", filepath.Dir(path))
	}
	stage, err := stageBinary("", data)
	if err != nil {
		return err
	}
	defer os.Remove(stage)

	fmt.Fprintf(os.Stderr, "fuckyourcli: %s needs root, asking sudo to install the new binary\n", filepath.Dir(path))
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".fyc-update-%d", os.Getpid()))
	// $1 = staged file, $2 = final path, $3 = temp name inside the target dir.
	const script = `set -e
trap 'rm -f "$3"' EXIT
cat "$1" > "$3"
chmod 0755 "$3"
chown 0:0 "$3"
mv -f "$3" "$2"`
	cmd := exec.Command("sudo", "sh", "-c", script, "fyc-update", stage, path, tmp)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo could not replace %s: %w", path, err)
	}
	return nil
}

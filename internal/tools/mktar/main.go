// Command mktar writes a deterministic tar stream of the given files to stdout.
//
// Entries carry only their base name, mode 0755 and zeroed mtime/uid/gid, so
// the same inputs always produce the same bytes. GNU tar and bsdtar disagree on
// the flags needed for that, hence this tool.
//
//	go run ./internal/tools/mktar FILE... | xz -9 -c > out.tar.xz
package main

import (
	"archive/tar"
	"bufio"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mktar FILE...")
		os.Exit(2)
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "mktar: %v\n", err)
		os.Exit(1)
	}
}

func run(paths []string) error {
	out := bufio.NewWriter(os.Stdout)
	tw := tar.NewWriter(out)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     filepath.Base(path),
			Mode:     0755,
			Size:     int64(len(data)),
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return out.Flush()
}

package updater

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeArchiveRejectsTraversalAndUnexpectedRoots(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "components/../../data/qoder.db", "components\\evil", "components/a:stream", "data/qoder.db", "components/CON.txt", "components/dir./a", "components/a /x", "qoder-api-proxy.exe/../secret"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			f, err := os.Create(filepath.Join(root, "bad.zip"))
			if err != nil {
				t.Fatal(err)
			}
			z := zip.NewWriter(f)
			w, _ := z.Create(name)
			_, _ = w.Write([]byte("bad"))
			_ = z.Close()
			_ = f.Close()
			if _, err := extractNative(filepath.Join(root, "bad.zip"), filepath.Join(root, "staging"), "v9.0.2"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestNativeBackendPathsAndMetadata(t *testing.T) {
	rootPath := t.TempDir()
	if !pathsOverlap(rootPath, filepath.Join(rootPath, "child")) || pathsOverlap(filepath.Join(rootPath, "a"), filepath.Join(rootPath, "ab")) {
		t.Fatal("native isolation directory check is incorrect")
	}

	if safeNativeName("runtime/accounts/auth.json") || safeNativeName("data/qoder.db") {
		t.Fatal("mutable account material must never be in application root")
	}
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "native-manifest.json"), []byte(`{"format":1,"backend":"docker","os":"windows","arch":"amd64","version":"v9.0.2"}`), 0600)
	if _, err := verifyNativeTree(root, "v9.0.2"); err == nil {
		t.Fatal("Docker package accepted by native backend")
	}
}

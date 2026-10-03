package resources

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// A configured file that cannot be used is reported without its path or
// name, which are configured text: a missing file (still recognizable as
// missing), a directory and a name without an accepted extension.
func TestLocalFileErrorsNameNoPath(t *testing.T) {
	const marker = "path-marker-synthetic-0123"
	dir := filepath.Join(t.TempDir(), marker)
	require.NoError(t, os.Mkdir(dir, 0o700))
	directory := filepath.Join(dir, marker+".png")
	require.NoError(t, os.Mkdir(directory, 0o700))
	missing := filepath.Join(dir, "absent-"+marker+".png")
	wrongExtension := filepath.Join(dir, marker+".txt")

	readers := map[string]func(string) error{
		"client logo": func(source string) error {
			_, _, err := readClientLogoSource(source)
			return err
		},
		"application image": func(source string) error {
			_, _, err := readApplicationImageSource(client.ApplicationImage("logo"), source)
			return err
		},
		"profile picture": func(source string) error {
			_, err := readUserProfilePicture(source)
			return err
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			for label, source := range map[string]string{"missing": missing, "directory": directory, "wrong extension": wrongExtension} {
				err := read(source)
				require.Error(t, err, label)
				assert.NotContains(t, err.Error(), marker, label)
				if label == "missing" && name != "profile picture" {
					assert.ErrorIs(t, err, fs.ErrNotExist, "a missing file is still recognized")
				}
			}
		})
	}
}

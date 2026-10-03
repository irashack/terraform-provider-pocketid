package resources

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

func profilePictureTestImage(w, h int) image.Image {
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.Gray{Y: 200})
	return img
}

func profilePictureTestFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, content, 0o600))
	return p
}

func profilePictureEncode(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case "png":
		require.NoError(t, png.Encode(&buf, profilePictureTestImage(w, h)))
	case "jpeg":
		require.NoError(t, jpeg.Encode(&buf, profilePictureTestImage(w, h), nil))
	case "gif":
		require.NoError(t, gif.Encode(&buf, profilePictureTestImage(w, h), nil))
	}
	return buf.Bytes()
}

func TestReadUserProfilePicture_AcceptsTheFormatsItCanCheck(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			content := profilePictureEncode(t, format, 60, 40)
			// The file name does not matter: the format comes from the content.
			file, err := readUserProfilePicture(profilePictureTestFile(t, "whatever.dat", content))
			require.NoError(t, err)
			digest := sha256.Sum256(content)
			assert.Equal(t, hex.EncodeToString(digest[:]), file.SHA256)
			assert.Equal(t, content, file.Content)
			assert.Equal(t, "profile-picture."+format, file.Name)
		})
	}
}

// WebP and BMP are decoded by Pocket ID (checked against 2.17) but not by this
// provider: they pass on their signature, with the server left to judge them.
func TestReadUserProfilePicture_PassesWebPAndBMPOn(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	require.NoError(t, err)
	file, err := readUserProfilePicture(profilePictureTestFile(t, "p", webp))
	require.NoError(t, err)
	assert.Equal(t, "image/webp", file.ContentType)

	bmp := make([]byte, 70)
	copy(bmp, "BM")
	file, err = readUserProfilePicture(profilePictureTestFile(t, "p", bmp))
	require.NoError(t, err)
	assert.Equal(t, "image/bmp", file.ContentType)
}

func TestReadUserProfilePicture_PixelLimit(t *testing.T) {
	// 4000x4000 is exactly the 16,000,000 pixels Pocket ID allows; 4001x4000 is over.
	_, err := readUserProfilePicture(profilePictureTestFile(t, "ok.png", profilePictureEncode(t, "png", 4000, 4000)))
	require.NoError(t, err)

	_, err = readUserProfilePicture(profilePictureTestFile(t, "big.png", profilePictureEncode(t, "png", 4001, 4000)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "4001x4000")
	assert.Contains(t, err.Error(), "16000000")
}

func TestReadUserProfilePicture_Refusals(t *testing.T) {
	dir := t.TempDir()
	_, err := readUserProfilePicture(filepath.Join(dir, "absent.png"))
	assert.ErrorIs(t, err, errProfilePictureMissing)

	_, err = readUserProfilePicture(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	_, err = readUserProfilePicture(profilePictureTestFile(t, "empty.png", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = readUserProfilePicture(profilePictureTestFile(t, "notes.png", []byte("this is not an image")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an image Pocket ID can read")

	// A PNG signature followed by nothing usable.
	_, err = readUserProfilePicture(profilePictureTestFile(t, "broken.png", append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 30)...)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a readable image")

	tooBig := make([]byte, client.UserProfilePictureMaxBytes+1)
	_, err = readUserProfilePicture(profilePictureTestFile(t, "huge.png", tooBig))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

// An error never echoes the file's content or the path the OS reports.
func TestReadUserProfilePicture_ErrorsDoNotEchoContentOrPath(t *testing.T) {
	secret := "correct-horse-battery-staple"
	p := profilePictureTestFile(t, "token.png", []byte(secret))
	_, err := readUserProfilePicture(p)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)

	require.NoError(t, os.Chmod(p, 0))
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if _, openErr := os.Open(p); openErr != nil && !errors.Is(openErr, os.ErrNotExist) { // not running as root
		_, err = readUserProfilePicture(p)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), p)
	}
}

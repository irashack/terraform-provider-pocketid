package resources

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registers the decoder image.DecodeConfig uses
	_ "image/jpeg" // registers the decoder image.DecodeConfig uses
	_ "image/png"  // registers the decoder image.DecodeConfig uses
	"io"
	"io/fs"
	"net/http"
	"os"

	"github.com/irashack/terraform-provider-pocketid/internal/client"
)

// userProfilePictureFile is a profile picture file read for upload.
type userProfilePictureFile struct {
	Content []byte
	// SHA256 is the hex digest of Content.
	SHA256 string
	// Name and ContentType describe the file to the server, which ignores both
	// and decodes the image itself.
	Name        string
	ContentType string
}

// errProfilePictureMissing marks a source file that does not exist (yet).
var errProfilePictureMissing = errors.New("the file does not exist")

// profilePictureProblem is why a source file cannot be uploaded. Its text names
// the problem and never any of the file's content.
type profilePictureProblem struct{ reason string }

func (p *profilePictureProblem) Error() string { return p.reason }

func profilePictureRefusal(format string, args ...any) error {
	return &profilePictureProblem{reason: fmt.Sprintf(format, args...)}
}

// readUserProfilePicture reads and checks the file at path as far as the
// provider can before the server does. Pocket ID decodes PNG, JPEG, GIF, WebP
// and BMP (checked against 2.17; it may take more) and refuses an image of more
// than 16 million pixels from 2.15 on; it sets no limit on the file's size, so
// the provider's own 10 MiB upload limit applies. A PNG, JPEG or GIF is checked
// for its dimensions and then decoded completely, as the server does. A file
// whose format this provider cannot decode itself (WebP, BMP, TIFF) is passed
// on with only its size checked: the server decides.
//
// A file that does not exist gives an error satisfying
// errors.Is(err, errProfilePictureMissing).
func readUserProfilePicture(path string) (*userProfilePictureFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errProfilePictureMissing
		}
		return nil, profilePictureRefusal("the file cannot be read (%s)", describeFileError(err))
	}
	if !info.Mode().IsRegular() {
		return nil, profilePictureRefusal("the path is not a regular file")
	}
	if info.Size() == 0 {
		return nil, profilePictureRefusal("the file is empty")
	}
	if info.Size() > client.UserProfilePictureMaxBytes {
		return nil, profilePictureRefusal("the file is %d bytes, over the %d-byte limit of an upload", info.Size(), client.UserProfilePictureMaxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, profilePictureRefusal("the file cannot be read (%s)", describeFileError(err))
	}
	defer func() { _ = file.Close() }()
	// One byte more than the limit, so a file that grew since the stat is caught.
	content, err := io.ReadAll(io.LimitReader(file, client.UserProfilePictureMaxBytes+1))
	if err != nil {
		return nil, profilePictureRefusal("the file cannot be read (%s)", describeFileError(err))
	}
	if int64(len(content)) > client.UserProfilePictureMaxBytes {
		return nil, profilePictureRefusal("the file is over the %d-byte limit of an upload", client.UserProfilePictureMaxBytes)
	}

	contentType := http.DetectContentType(content)
	name := "profile-picture"
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	switch {
	case err == nil:
		if config.Width <= 0 || config.Height <= 0 {
			return nil, profilePictureRefusal("the image has no pixels")
		}
		if int64(config.Width)*int64(config.Height) > client.UserProfilePictureMaxPixels {
			return nil, profilePictureRefusal("the image is %dx%d pixels; Pocket ID accepts at most %d pixels in total (about 4000x4000)", config.Width, config.Height, client.UserProfilePictureMaxPixels)
		}
		// DecodeConfig read only the header, so a file cut short, or whose
		// image data is damaged, got this far. Pocket ID decodes the whole
		// image and answers an error for such a file after the apply has
		// started: decode it here as well, which the pixel limit above keeps
		// to a bounded amount of memory.
		if _, _, decodeErr := image.Decode(bytes.NewReader(content)); decodeErr != nil {
			return nil, profilePictureRefusal("the %s image is incomplete or damaged and cannot be decoded", format)
		}
		name += "." + format
	case errors.Is(err, image.ErrFormat):
		// Not a format this provider decodes. WebP and BMP are sniffed by
		// their signatures; TIFF is not, so its byte-order marks are checked.
		switch {
		case contentType == "image/webp":
			name += ".webp"
		case contentType == "image/bmp":
			name += ".bmp"
		case bytes.HasPrefix(content, []byte("II*\x00")) || bytes.HasPrefix(content, []byte("MM\x00*")):
			contentType, name = "image/tiff", name+".tiff"
		default:
			return nil, profilePictureRefusal("the file is not an image Pocket ID can read (PNG, JPEG, GIF, WebP or BMP)")
		}
	default:
		return nil, profilePictureRefusal("the file is not a readable image")
	}

	digest := sha256.Sum256(content)
	return &userProfilePictureFile{
		Content:     content,
		SHA256:      hex.EncodeToString(digest[:]),
		Name:        name,
		ContentType: contentType,
	}, nil
}

// describeFileError names why a file operation failed without echoing the
// operating system's message, which includes the path.
// localFileError reports a failure to open or read a configured file without
// the error Go gives, which quotes the path (configured text): it keeps only
// the kind of failure, and errors.Is still finds fs.ErrNotExist.
func localFileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the file does not exist: %w", fs.ErrNotExist)
	}
	return fmt.Errorf("the file cannot be read (%s)", describeFileError(err))
}

func describeFileError(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	default:
		return "an I/O error"
	}
}

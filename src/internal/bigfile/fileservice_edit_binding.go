package bigfile

import (
	"context"
	"errors"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/manualedit"
	"novera/internal/bigfile/session"
)

// editBoundSource adapts FileService's exact full digest plus bounded block map
// to manualedit without introducing a package cycle.
type editBoundSource struct {
	expected *documentSourceExpectation
	doc      *document.FileDocument
}

var captureEditSourceExpectation = captureDocumentSourceExpectation

func (source *editBoundSource) Size() int64 {
	if source == nil || source.expected == nil {
		return 0
	}
	return source.expected.size
}

func (source *editBoundSource) VerifiedReader(ctx context.Context) (document.ReaderAtSize, error) {
	if source == nil || source.expected == nil || source.doc == nil {
		return nil, errors.New("prepared edit source is unavailable")
	}
	return newVerifiedDocumentReader(ctx, source.expected, source.doc)
}

func (source *editBoundSource) ValidateContext(ctx context.Context, progress func(int64, int64)) error {
	if source == nil || source.expected == nil || source.doc == nil {
		return errors.New("prepared edit source is unavailable")
	}
	return source.expected.validateContext(ctx, source.doc, progress)
}

// captureExactEditSession performs the one full, cancellable source pass
// required before the first edited byte can be staged. Installation remains a
// separate job-owned commit so cancellation can still win after fingerprinting
// but before prepared state becomes visible.
func captureExactEditSession(ctx context.Context, file *session.File, progress func(int64, int64)) (*manualedit.Session, error) {
	if file == nil || file.Doc == nil {
		return nil, errors.New("open file is required")
	}
	if file.Edit != nil {
		return nil, errors.New("edit session is already installed")
	}
	expected, err := captureEditSourceExpectation(ctx, file.Doc, file.Path, progress)
	if err != nil {
		return nil, err
	}
	return manualedit.NewSourceBoundSession(
		file.Doc.Size(),
		file.Path,
		file.Generation,
		&editBoundSource{expected: expected, doc: file.Doc},
		manualedit.DefaultLimits(),
	)
}

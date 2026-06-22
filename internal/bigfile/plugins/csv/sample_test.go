package csv

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBoundedSampleOperationsHonorCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := strings.NewReader("id,name\n1,Ada\n")

	if _, err := InspectReaderContext(ctx, input, InspectOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("InspectReaderContext err = %v, want context.Canceled", err)
	}
	if _, err := InferSchemaContext(ctx, strings.NewReader("id,name\n1,Ada\n"), SchemaOptions{HasHeader: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("InferSchemaContext err = %v, want context.Canceled", err)
	}
	if _, err := PreviewRowsContext(ctx, strings.NewReader("id,name\n1,Ada\n"), PreviewOptions{HasHeader: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PreviewRowsContext err = %v, want context.Canceled", err)
	}
	if _, err := BuildColumnGuideContext(ctx, strings.NewReader("id,name\n1,Ada\n"), ColumnGuideOptions{HasHeader: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildColumnGuideContext err = %v, want context.Canceled", err)
	}
	if _, err := PreviewProjectedColumnsContext(ctx, strings.NewReader("id,name\n1,Ada\n"), ProjectPreviewOptions{Columns: []int{0}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PreviewProjectedColumnsContext err = %v, want context.Canceled", err)
	}
}

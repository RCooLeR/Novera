package bigfile

import (
	"strings"
	"testing"
)

func TestSampleInsertRows(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		maxRows int
		want    string
	}{
		{
			name:    "first two of four tuples",
			data:    "INSERT INTO `t` VALUES (1,'a'),(2,'b'),(3,'c');\nINSERT INTO `t` VALUES (4,'d');\n",
			maxRows: 2,
			want:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\n",
		},
		{
			name:    "across statements",
			data:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\nINSERT INTO `t` VALUES (3,'c'),(4,'d');\n",
			maxRows: 3,
			want:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\nINSERT INTO `t` VALUES (3,'c');\n",
		},
		{
			name:    "paren inside string is ignored",
			data:    "INSERT INTO `t` VALUES (1,'a)b'),(2,'c');\n",
			maxRows: 1,
			want:    "INSERT INTO `t` VALUES (1,'a)b');\n",
		},
		{
			name:    "escaped quote inside string",
			data:    "INSERT INTO `t` VALUES (1,'a\\'b'),(2,'c');\n",
			maxRows: 1,
			want:    "INSERT INTO `t` VALUES (1,'a\\'b');\n",
		},
		{
			name:    "fewer rows than requested keeps terminator",
			data:    "INSERT INTO `t` VALUES (1,'a');\n",
			maxRows: 10,
			want:    "INSERT INTO `t` VALUES (1,'a');\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.data)
			got, err := sampleInsertRows(r, 0, int64(len(tc.data)), tc.maxRows)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", string(got), tc.want)
			}
		})
	}
}

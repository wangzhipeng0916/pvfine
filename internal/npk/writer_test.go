package npk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/korean"
)

type testNPKMember struct {
	raw  [256]byte
	data []byte
}

func encryptTestName(t *testing.T, name string) [256]byte {
	t.Helper()
	return encryptTestNameBytes(t, []byte(name))
}

func encryptTestNameBytes(t *testing.T, plain []byte) [256]byte {
	t.Helper()
	if len(plain) > 256 {
		t.Fatalf("name too long for one entry: %d bytes", len(plain))
	}
	var raw [256]byte
	for index := range raw {
		var value byte
		if index < len(plain) {
			value = plain[index]
		}
		raw[index] = value ^ npkFilenameKey[index%len(npkFilenameKey)]
	}
	return raw
}

// buildTestNPK lays out an NPK from scratch instead of calling CopySubset so
// the writer is checked against an independently built container.
func buildTestNPK(members []testNPKMember) []byte {
	size := npkHeaderSize + npkEntrySize*len(members)
	offsets := make([]int, len(members))
	for index, member := range members {
		size = (size + 3) &^ 3
		offsets[index] = size
		size += len(member.data)
	}
	data := make([]byte, size)
	copy(data, npkMagic)
	binary.LittleEndian.PutUint32(data[16:], uint32(len(members)))
	for index, member := range members {
		base := npkHeaderSize + npkEntrySize*index
		binary.LittleEndian.PutUint32(data[base:], uint32(offsets[index]))
		binary.LittleEndian.PutUint32(data[base+4:], uint32(len(member.data)))
		copy(data[base+8:base+npkEntrySize], member.raw[:])
		copy(data[offsets[index]:], member.data)
	}
	return data
}

func writeTestNPKFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.npk")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readTestRange(t *testing.T, r io.ReaderAt, offset, size int64) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := r.ReadAt(data, offset); err != nil {
		t.Fatalf("read %d bytes at %d: %v", size, offset, err)
	}
	return data
}

func TestCopySubsetRoundTrip(t *testing.T) {
	members := []testNPKMember{
		{raw: encryptTestName(t, "sprite/item/a.img"), data: []byte("alpha")},
		{raw: encryptTestName(t, "sprite/item/b.img"), data: []byte("bravo-bytes")},
		{raw: encryptTestName(t, "sprite/item/c.img"), data: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
	}
	sourcePath := writeTestNPKFile(t, buildTestNPK(members))
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Entries) != len(members) {
		t.Fatalf("source entries = %d, want %d", len(source.Entries), len(members))
	}
	for index, entry := range source.Entries {
		if entry.Name == "" || entry.Size != int64(len(members[index].data)) {
			t.Fatalf("source entry %d = %#v", index, entry)
		}
	}

	srcReader, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer srcReader.Close()

	dstPath := filepath.Join(t.TempDir(), "copy.npk")
	dstFile, err := os.Create(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := CopySubset(dstFile, srcReader, source.Entries); err != nil {
		_ = dstFile.Close()
		t.Fatal(err)
	}
	if err := dstFile.Close(); err != nil {
		t.Fatal(err)
	}

	copied, err := Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Entries) != len(source.Entries) {
		t.Fatalf("copied entries = %d, want %d", len(copied.Entries), len(source.Entries))
	}
	dstReader, err := os.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dstReader.Close()

	for index := range source.Entries {
		oldEntry, newEntry := source.Entries[index], copied.Entries[index]
		if newEntry.Name != oldEntry.Name {
			t.Fatalf("entry %d name = %q, want %q", index, newEntry.Name, oldEntry.Name)
		}
		if newEntry.Size != oldEntry.Size {
			t.Fatalf("entry %d size = %d, want %d", index, newEntry.Size, oldEntry.Size)
		}
		original := readTestRange(t, srcReader, oldEntry.Offset, oldEntry.Size)
		written := readTestRange(t, dstReader, newEntry.Offset, newEntry.Size)
		if !bytes.Equal(written, original) {
			t.Fatalf("entry %d bytes differ from source", index)
		}
		if !bytes.Equal(written, members[index].data) {
			t.Fatalf("entry %d bytes differ from fixture", index)
		}

		rawBase := int64(npkHeaderSize + npkEntrySize*index + 8)
		oldRaw := readTestRange(t, srcReader, rawBase, 256)
		newRaw := readTestRange(t, dstReader, rawBase, 256)
		if !bytes.Equal(newRaw, oldRaw) {
			t.Fatalf("entry %d encrypted name differs from source", index)
		}
		if !bytes.Equal(newRaw, members[index].raw[:]) {
			t.Fatalf("entry %d encrypted name differs from fixture", index)
		}
		if newEntry.RawName != members[index].raw {
			t.Fatalf("entry %d RawName differs from fixture", index)
		}
	}
}

func TestCopySubsetSingleMember(t *testing.T) {
	members := []testNPKMember{
		{raw: encryptTestName(t, "sprite/item/a.img"), data: make([]byte, 7)},
		{raw: encryptTestName(t, "sprite/item/b.img"), data: []byte("only-this-one")},
		{raw: encryptTestName(t, "sprite/item/c.img"), data: []byte("unused")},
	}
	sourcePath := writeTestNPKFile(t, buildTestNPK(members))
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	srcReader, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer srcReader.Close()

	var out bytes.Buffer
	if err := CopySubset(&out, srcReader, source.Entries[1:2]); err != nil {
		t.Fatal(err)
	}

	copied, err := Open(writeTestNPKFile(t, out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Entries) != 1 {
		t.Fatalf("copied entries = %d, want 1", len(copied.Entries))
	}
	entry := copied.Entries[0]
	if entry.Name != source.Entries[1].Name {
		t.Fatalf("name = %q, want %q", entry.Name, source.Entries[1].Name)
	}
	if entry.Size != source.Entries[1].Size {
		t.Fatalf("size = %d, want %d", entry.Size, source.Entries[1].Size)
	}
	// 20-byte header plus one 264-byte entry, already 4-byte aligned.
	const wantOffset = int64(npkHeaderSize + npkEntrySize)
	if entry.Offset != wantOffset {
		t.Fatalf("offset = %d, want %d", entry.Offset, wantOffset)
	}
	written := readTestRange(t, bytes.NewReader(out.Bytes()), entry.Offset, entry.Size)
	if !bytes.Equal(written, members[1].data) {
		t.Fatalf("member bytes = %q, want %q", written, members[1].data)
	}
}

func TestCopySubsetAlignment(t *testing.T) {
	for _, size := range []int{1, 2, 3, 5, 7, 10, 15} {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			members := []testNPKMember{
				{raw: encryptTestName(t, "a.img"), data: bytes.Repeat([]byte{0xab}, size)},
				{raw: encryptTestName(t, "b.img"), data: []byte{1, 2, 3}},
			}
			sourcePath := writeTestNPKFile(t, buildTestNPK(members))
			source, err := Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			srcReader, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer srcReader.Close()

			var out bytes.Buffer
			if err := CopySubset(&out, srcReader, source.Entries); err != nil {
				t.Fatal(err)
			}
			copied, err := Open(writeTestNPKFile(t, out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			first, second := copied.Entries[0], copied.Entries[1]
			if first.Offset != int64(npkHeaderSize+2*npkEntrySize) {
				t.Fatalf("first offset = %d", first.Offset)
			}
			if second.Offset%4 != 0 {
				t.Fatalf("second offset = %d, not 4-byte aligned", second.Offset)
			}
			if second.Offset < first.Offset+first.Size {
				t.Fatalf("second offset %d overlaps first member ending at %d", second.Offset, first.Offset+first.Size)
			}
			padding := out.Bytes()[first.Offset+first.Size : second.Offset]
			for _, value := range padding {
				if value != 0 {
					t.Fatalf("padding = %v, want zeros", padding)
				}
			}
			written := readTestRange(t, bytes.NewReader(out.Bytes()), second.Offset, second.Size)
			if !bytes.Equal(written, members[1].data) {
				t.Fatalf("second member bytes = %v, want %v", written, members[1].data)
			}
		})
	}
}

func TestCopySubsetEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := CopySubset(&out, bytes.NewReader(buildTestNPK(nil)), nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != npkHeaderSize {
		t.Fatalf("empty container = %d bytes, want %d", out.Len(), npkHeaderSize)
	}
	copied, err := Open(writeTestNPKFile(t, out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Entries) != 0 {
		t.Fatalf("empty container has %d entries", len(copied.Entries))
	}
	if copied.Size != npkHeaderSize {
		t.Fatalf("empty container size = %d, want %d", copied.Size, npkHeaderSize)
	}
}

func TestCopySubsetNonASCIIName(t *testing.T) {
	const name = "sprite/아이템.img"
	encoded, err := korean.EUCKR.NewEncoder().Bytes([]byte(name))
	if err != nil {
		t.Fatal(err)
	}
	raw := encryptTestNameBytes(t, encoded)
	members := []testNPKMember{{raw: raw, data: []byte("payload")}}

	sourcePath := writeTestNPKFile(t, buildTestNPK(members))
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if source.Entries[0].Name != name {
		t.Fatalf("decoded name = %q, want %q", source.Entries[0].Name, name)
	}
	srcReader, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer srcReader.Close()

	var out bytes.Buffer
	if err := CopySubset(&out, srcReader, source.Entries); err != nil {
		t.Fatal(err)
	}
	copied, err := Open(writeTestNPKFile(t, out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if copied.Entries[0].Name != name {
		t.Fatalf("copied name = %q, want %q", copied.Entries[0].Name, name)
	}
	if copied.Entries[0].RawName != raw {
		t.Fatal("copied RawName differs from source")
	}
	rawBase := int64(npkHeaderSize + 8)
	written := readTestRange(t, bytes.NewReader(out.Bytes()), rawBase, 256)
	if !bytes.Equal(written, raw[:]) {
		t.Fatal("copied encrypted name differs from source")
	}
}

func TestCopySubsetRejectsInt32Overflow(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
	}{
		{name: "past int32", entry: Entry{Name: "huge.img", Size: math.MaxInt32}},
		{name: "negative size", entry: Entry{Name: "bad.img", Size: -1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := CopySubset(&out, bytes.NewReader(nil), []Entry{test.entry}); err == nil {
				t.Fatal("invalid member should fail")
			}
			if out.Len() != 0 {
				t.Fatalf("wrote %d bytes before detecting the invalid member", out.Len())
			}
		})
	}
}

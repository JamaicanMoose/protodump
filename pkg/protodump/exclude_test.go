package protodump

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestExcludePackages(t *testing.T) {
	fd1 := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("foo.proto"),
		Package: proto.String("com.example.foo"),
	}
	fd2 := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("bar.proto"),
		Package: proto.String("com.example.bar"),
	}
	fd3 := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("baz.proto"),
		Package: proto.String("google.protobuf"),
	}

	b1, err := proto.Marshal(fd1)
	assert.NoError(t, err)
	b2, err := proto.Marshal(fd2)
	assert.NoError(t, err)
	b3, err := proto.Marshal(fd3)
	assert.NoError(t, err)

	def1, err := NewFromBytes(b1)
	assert.NoError(t, err)
	def2, err := NewFromBytes(b2)
	assert.NoError(t, err)
	def3, err := NewFromBytes(b3)
	assert.NoError(t, err)

	assert.Equal(t, "com.example.foo", def1.Package())
	assert.Equal(t, "com.example.bar", def2.Package())
	assert.Equal(t, "google.protobuf", def3.Package())

	defs := []*ProtoDefinition{def1, def2, def3}

	t.Run("no exclusions", func(t *testing.T) {
		res := ExcludePackages(defs, nil)
		assert.Len(t, res, 3)
	})

	t.Run("exclude single package", func(t *testing.T) {
		res := ExcludePackages(defs, []string{"google.protobuf"})
		assert.Len(t, res, 2)
		assert.Equal(t, "com.example.foo", res[0].Package())
		assert.Equal(t, "com.example.bar", res[1].Package())
	})

	t.Run("exclude multiple packages with whitespace and leading dot", func(t *testing.T) {
		res := ExcludePackages(defs, []string{" .com.example.foo ", "google.protobuf"})
		assert.Len(t, res, 1)
		assert.Equal(t, "com.example.bar", res[0].Package())
	})
}

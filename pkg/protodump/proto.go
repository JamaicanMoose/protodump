package protodump

import (
	"fmt"
	"path"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)
import "github.com/jhump/protoreflect/v2/protoprint"

type ProtoDefinition struct {
	pb         *descriptorpb.FileDescriptorProto
	descriptor protoreflect.FileDescriptor
}

func (pd *ProtoDefinition) String() (string, error) {
	printer := protoprint.Printer{}
	protostr, err := printer.PrintProtoToString(desc)
	if err != nil {
		return nil, err
	}
	return protostr
}

func (pd *ProtoDefinition) Filename() string {
	goPackage := pd.pb.GetOptions().GetGoPackage()
	index := strings.Index(goPackage, ";")
	if index == -1 {
		return pd.descriptor.Path()
	}

	return path.Join(goPackage[:index], path.Base(pd.descriptor.Path()))
}

func NewFromBytes(payload []byte) (*ProtoDefinition, error) {
	var pb descriptorpb.FileDescriptorProto
	err := proto.Unmarshal(payload, &pb)
	if err != nil {
		return nil, fmt.Errorf("Couldn't unmarshal proto: %w", err)
	}

	return NewFromDescriptor(&pb)
}

func FixGoogleBinaryDescriptorProto(fd *descriptorpb.FileDescriptorProto) {
	// Replace dependency if present
	for i, dep := range fd.Dependency {
		if dep == "net/proto2/proto/descriptor.proto" {
			fd.Dependency[i] = "google/protobuf/descriptor.proto"
		}
	}

	// Replace extendees
	for _, ext := range fd.Extension {
		switch ext.GetExtendee() {
		case ".proto2.FileOptions":
			ext.Extendee = proto.String(".google.protobuf.FileOptions")
		case ".proto2.EnumOptions":
			ext.Extendee = proto.String(".google.protobuf.EnumOptions")
		case ".proto2.EnumValueOptions":
			ext.Extendee = proto.String(".google.protobuf.EnumValueOptions")
		case ".proto2.MessageOptions":
			ext.Extendee = proto.String(".google.protobuf.MessageOptions")
		case ".proto2.FieldOptions":
			ext.Extendee = proto.String(".google.protobuf.FieldOptions")
		case ".proto2.OneofOptions":
			ext.Extendee = proto.String(".google.protobuf.OneofOptions")
		case ".proto2.ExtensionRangeOptions":
			ext.Extendee = proto.String(".google.protobuf.ExtensionRangeOptions")
		case ".proto2.ServiceOptions":
			ext.Extendee = proto.String(".google.protobuf.ServiceOptions")
		case ".proto2.MethodOptions":
			ext.Extendee = proto.String(".google.protobuf.MethodOptions")
		}
	}
}

func FixUnsupportedEdition(fd *descriptorpb.FileDescriptorProto) {
	if fd.Edition != nil {
		if _, ok := descriptorpb.Edition_name[int32(*fd.Edition)]; !ok {
			fd.Edition = descriptorpb.Edition_EDITION_UNSTABLE.Enum()
		}
	}
}

func NewFromDescriptor(pb *descriptorpb.FileDescriptorProto) (*ProtoDefinition, error) {
	FixGoogleBinaryDescriptorProto(pb)
	FixUnsupportedEdition(pb)
	fileOptions := protodesc.FileOptions{AllowUnresolvable: true}
	descriptor, err := fileOptions.New(pb, &protoregistry.Files{})

	if err != nil {
		return nil, fmt.Errorf("Couldn't create FileDescriptor: %w", err)
	}

	pd := ProtoDefinition{
		pb:         pb,
		descriptor: descriptor,
	}

	return &pd, nil
}

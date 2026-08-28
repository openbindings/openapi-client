package openapiclient

import (
	"context"
	"reflect"
	"testing"
)

func TestOpenAPI32InboundOperationInventoryIncludesCallbacksWebhooksAndAdditionalMethods(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`{
  "openapi":"3.2.0",
  "info":{"title":"inbound inventory","version":"1"},
  "paths":{"/jobs":{"post":{"callbacks":{"done":{"{$request.body#/url}":{
    "post":{"operationId":"receiveDone","responses":{"200":{}}},
    "additionalOperations":{"REPORT":{"operationId":"reportDone","responses":{"204":{}}}}
  }}},"responses":{"202":{}}}}},
  "webhooks":{"changed":{"query":{"operationId":"queryChange","responses":{"200":{}}}}}
}`)}, ArtifactLoadOptions{AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}

	inventory := artifact.InboundOperationInventory()
	var refs []string
	for _, disposition := range inventory {
		if disposition.Err != nil || disposition.Target == nil {
			t.Fatalf("inbound disposition = %#v", disposition)
		}
		refs = append(refs, disposition.Reference.Ref)
	}
	want := []string{
		"#/paths/~1jobs/post/callbacks/done/{$request.body#~1url}/post",
		"#/paths/~1jobs/post/callbacks/done/{$request.body#~1url}/additionalOperations/REPORT",
		"#/webhooks/changed/query",
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("inbound refs = %#v, want %#v", refs, want)
	}
}

package agents

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type judgeRoundTripper func(*http.Request) (*http.Response, error)

func (f judgeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEvaluationJudgeReturnsVersionableEvidence(t *testing.T) {
	client := New("https://model.test", "test", "judge-model")
	client.http.Transport = judgeRoundTripper(func(*http.Request) (*http.Response, error) {
		body := `{"id":"resp_judge","status":"completed","usage":{"input_tokens":12,"output_tokens":6},"output":[{"type":"message","content":[{"type":"output_text","text":"{\"dimensions\":[{\"name\":\"completeness\",\"score\":0.8,\"evidence\":\"one gap\"}],\"summary\":\"reviewed\"}"}]}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	result, trace, err := client.JudgeEvaluation(context.Background(), "run-1", evaluationJudgeInstructions,
		json.RawMessage(`{"candidate_output":{}}`), evaluationJudgeSchema)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dimensions) != 1 || result.Dimensions[0].Score != .8 || trace.ProviderResponseID != "resp_judge" {
		t.Fatalf("result=%#v trace=%#v", result, trace)
	}
}

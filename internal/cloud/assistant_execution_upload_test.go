package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dropfile/HankServerside/internal/assistant"
	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	assistantschema "github.com/dropfile/HankServerside/schemas/assistant"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecutionStagedUploadApprovalAndChunks(t *testing.T) {
	s, task := executionTaskFixture(t)
	s.ConfigureNoteAttachmentStorage(t.TempDir())
	s.assistantAI.ExecutionEnabled = true
	ctx := context.Background()
	content := strings.Repeat("upload bytes", 30000)
	sum := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(sum[:])
	upload := func(user, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/staging", strings.NewReader(body))
		request.Header.Set("X-Hank-Filename", url.PathEscape("Exact file.txt"))
		request.Header.Set("X-Hank-Attachment-ID", "upload-1")
		request.Header.Set("X-Hank-Content-Type", "text/plain")
		request.Header.Set("X-Hank-Size-Bytes", strconv.Itoa(len(content)))
		request.Header.Set("X-Hank-Checksum-SHA256", checksum)
		recorder := httptest.NewRecorder()
		s.handleAssistantStageUpload(recorder, request, domain.Home{ID: task.HomeID}, authContext{User: domain.User{ID: user}}, task.SessionID)
		return recorder
	}
	if got := upload("foreign", content); got.Code == 201 {
		t.Fatal("foreign staged into conversation")
	}
	if got := upload(task.UserID, "partial"); got.Code == 201 {
		t.Fatal("partial accepted")
	}
	response := upload(task.UserID, content)
	if response.Code != 201 {
		t.Fatalf("stage: %d %s", response.Code, response.Body.String())
	}
	var stage domain.AssistantStage
	must(t, json.Unmarshal(response.Body.Bytes(), &stage))
	replay := upload(task.UserID, content)
	var repeated domain.AssistantStage
	must(t, json.Unmarshal(replay.Body.Bytes(), &repeated))
	if repeated.ID != stage.ID {
		t.Fatal("replayed upload duplicated stage")
	}
	registry, err := s.newAssistantExecutionTools(task.HomeID, task.UserID, task.SessionID)
	must(t, err)
	listing := executionItems(t, invokeExecution(t, registry, "attachments.list", map[string]any{}))
	if len(listing.Items) != 1 || listing.Items[0].StageID != stage.ID {
		t.Fatal("conversation lost staged handle")
	}
	home, err := s.store.GetHomeByID(ctx, task.HomeID)
	must(t, err)
	transferred := []byte{}
	writes := 0
	commands := []string{}
	caps := []string{"files.stat", "files.upload", protocol.CommandAssistantOperationExecute, protocol.CommandAssistantOperationStatus, protocol.CommandAssistantOperationStage, protocol.CapabilityAssistantJournalEpochPrefix + strings.Repeat("a", 32)}
	agentID := executionFakeAgent(t, s, home, caps, func(command protocol.RoutedCommand) any {
		commands = append(commands, command.Command)
		switch command.Command {
		case "files.stat":
			return protocol.FilesStatResponse{Item: protocol.FileItem{SourceID: "local", Path: "Work", IsDirectory: true}}
		case protocol.CommandAssistantOperationStage:
			var request protocol.AssistantOperationStageRequest
			if json.Unmarshal(command.Body, &request) != nil {
				return nil
			}
			if request.Offset != int64(len(transferred)) {
				return nil
			}
			transferred = append(transferred, request.Data...)
			return protocol.AssistantOperationStageResponse{Identity: request.Identity, NextOffset: int64(len(transferred))}
		default:
			var request protocol.AssistantOperationRequest
			if json.Unmarshal(command.Body, &request) != nil {
				return nil
			}
			outcome := "not_found"
			if command.Command == protocol.CommandAssistantOperationExecute {
				writes++
				outcome = "confirmed"
			}
			raw, _ := json.Marshal(protocol.AssistantOperationResult{Verification: "observed", Item: &protocol.FileItem{SourceID: "local", Path: "Work/Exact.txt", Size: int64(len(content)), ModifiedAt: time.Now().UTC()}})
			return protocol.AssistantOperationStatusResponse{Identity: request.Identity, Outcome: outcome, Result: raw}
		}
	})
	args, _ := json.Marshal(map[string]any{"agent_id": agentID, "source_id": "local", "path": "Work/Exact.txt", "stage_id": stage.ID})
	call := assistant.Call{ID: "upload", Tool: "files.upload", Version: 1, Arguments: args}
	task = prepareExecutionWrite(t, s, task, call)
	task, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	receipt, err := s.store.GetAssistantOperation(ctx, task.HomeID, task.UserID, executionOperationID(task.ID, call.ID))
	must(t, err)
	if receipt.Outcome != "confirmed" || writes != 1 || string(transferred) != content {
		t.Fatalf("upload not verified: %s writes=%d bytes=%d commands=%v", receipt.Outcome, writes, len(transferred), commands)
	}
	var result assistant.Result
	must(t, json.Unmarshal(receipt.Result, &result))
	must(t, assistantschema.Validate(result))
	_, err = s.advanceAssistantExecution(ctx, task, nil)
	must(t, err)
	if writes != 1 {
		t.Fatal("receipt replay duplicated upload")
	}
}

func TestExecutionStageRouteAuthenticationAndCSRF(t *testing.T) {
	s, task := executionTaskFixture(t)
	s.ConfigureNoteAttachmentStorage(t.TempDir())
	s.assistantAI.ExecutionEnabled = true
	now := time.Now().UTC()
	must(t, s.store.CreateSession(context.Background(), domain.AppSession{ID: "stage-auth", UserID: task.UserID, TokenHash: hashToken("stage-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	sum := sha256.Sum256([]byte("x"))
	for _, test := range []struct {
		name                 string
		cookie, csrf, bearer bool
		want                 int
	}{{"anonymous", false, false, false, 401}, {"cookie no csrf", true, false, false, 403}, {"cookie csrf", true, true, false, 201}, {"bearer", false, false, true, 201}} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/home/assistant/sessions/"+task.SessionID+"/staging", strings.NewReader("x"))
			request.Header.Set("X-Hank-Filename", "x.txt")
			request.Header.Set("X-Hank-Attachment-ID", "route-upload")
			request.Header.Set("X-Hank-Content-Type", "text/plain")
			request.Header.Set("X-Hank-Size-Bytes", "1")
			request.Header.Set("X-Hank-Checksum-SHA256", hex.EncodeToString(sum[:]))
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "stage-token"})
			}
			if test.csrf {
				request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "stage-csrf"})
				request.Header.Set(csrfHeaderName, "stage-csrf")
			}
			if test.bearer {
				request.Header.Set("Authorization", "Bearer stage-token")
			}
			recorder := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status %d want %d", recorder.Code, test.want)
			}
		})
	}
}

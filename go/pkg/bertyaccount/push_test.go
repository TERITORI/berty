package bertyaccount_test

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	proto "github.com/gogo/protobuf/proto"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"berty.tech/berty/v2/go/internal/testutil"
	"berty.tech/berty/v2/go/pkg/bertyaccount"
	"berty.tech/berty/v2/go/pkg/bertyprotocol"
	"berty.tech/berty/v2/go/pkg/messengertypes"
	"berty.tech/berty/v2/go/pkg/protocoltypes"
)

func TestPushDecryptStandalone(t *testing.T) {
	t.Skip("Push token is frequently not received by the second member")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*20)
	defer cancel()

	logger, cleanup := testutil.Logger(t)
	defer cleanup()

	dispatcher := testutil.NewPushMockedDispatcher(testutil.PushMockBundleID)
	dispatchers := []bertyprotocol.PushDispatcher{dispatcher}
	_, pushPK, pushHost, cancel := bertyprotocol.PushServerForTests(ctx, t, dispatchers, logger.Named("bty"))
	defer cancel()

	// prepare deps
	tempdir, err := ioutil.TempDir("", "berty-account")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	_, authServerSecret, err := box.GenerateKey(crand.Reader)
	require.NoError(t, err)

	// auth is not enforced
	_, authServerSK, err := ed25519.GenerateKey(crand.Reader)
	require.NoError(t, err)

	svc1RootDir := filepath.Join(tempdir, "root_1")
	svc1Token := "test_token_svc1"
	svc1Account1 := "acc_1_1"

	svc2RootDir := filepath.Join(tempdir, "root_2")
	svc2Account1 := "acc_2_1"

	// init service
	svc1, err := bertyaccount.NewService(&bertyaccount.Options{
		RootDirectory: svc1RootDir,
		Logger:        logger,
	})
	require.NoError(t, err)
	defer svc1.Close()

	_, err = svc1.PushPlatformTokenRegister(ctx, &bertyaccount.PushPlatformTokenRegister_Request{
		Receiver: &protocoltypes.PushServiceReceiver{
			TokenType: protocoltypes.PushTokenMQTT,
			BundleID:  testutil.PushMockBundleID,
			Token:     []byte(svc1Token),
		},
	})
	require.NoError(t, err)

	_, err = svc1.CreateAccount(ctx, &bertyaccount.CreateAccount_Request{
		AccountID:   svc1Account1,
		AccountName: svc1Account1,
	})
	require.NoError(t, err)

	// init service
	svc2, err := bertyaccount.NewService(&bertyaccount.Options{
		RootDirectory: svc2RootDir,
		Logger:        logger,
	})
	require.NoError(t, err)
	defer svc2.Close()

	_, err = svc2.CreateAccount(ctx, &bertyaccount.CreateAccount_Request{
		AccountID:   svc2Account1,
		AccountName: svc2Account1,
	})
	require.NoError(t, err)

	messenger1, err := svc1.GetMessengerClient()
	require.NoError(t, err)

	protocol1, err := svc1.GetProtocolClient()
	require.NoError(t, err)

	tokenIssuer, err := bertyprotocol.NewAuthTokenIssuer(authServerSecret[:], authServerSK)
	require.NoError(t, err)

	services := map[string]string{bertyprotocol.ServicePushID: pushHost}

	randomToken, err := bertyprotocol.IssueRandomToken(tokenIssuer, services)
	require.NoError(t, err)

	_, err = protocol1.DebugAuthServiceSetToken(ctx, &protocoltypes.DebugAuthServiceSetToken_Request{
		Token: &protocoltypes.AuthExchangeResponse{
			AccessToken: randomToken,
			Scope:       bertyprotocol.ServicePushID,
			Services:    services,
		},
	})
	require.NoError(t, err)

	_, err = protocol1.PushSetServer(ctx, &protocoltypes.PushSetServer_Request{
		Server: &protocoltypes.PushServer{
			ServerKey:   pushPK[:],
			ServiceAddr: pushHost,
		},
	})
	require.NoError(t, err)

	_, err = messenger1.PushSetAutoShare(ctx, &messengertypes.PushSetAutoShare_Request{Enabled: true})
	require.NoError(t, err)

	messenger2, err := svc2.GetMessengerClient()
	require.NoError(t, err)

	protocol2, err := svc2.GetProtocolClient()
	require.NoError(t, err)

	mess1Acc, err := messenger1.AccountGet(ctx, &messengertypes.AccountGet_Request{})
	require.NoError(t, err)

	mess2Acc, err := messenger2.AccountGet(ctx, &messengertypes.AccountGet_Request{})
	require.NoError(t, err)

	_, err = messenger1.ContactRequest(ctx, &messengertypes.ContactRequest_Request{
		Link: mess2Acc.Account.Link,
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 1)

	_, err = messenger2.ContactAccept(ctx, &messengertypes.ContactAccept_Request{
		PublicKey: mess1Acc.Account.PublicKey,
	})
	require.NoError(t, err)

	mess1PK, err := base64.RawURLEncoding.DecodeString(mess1Acc.Account.PublicKey)
	require.NoError(t, err)

	mess2PK, err := base64.RawURLEncoding.DecodeString(mess2Acc.Account.PublicKey)
	require.NoError(t, err)

	grpInf1, err := protocol1.GroupInfo(ctx, &protocoltypes.GroupInfo_Request{ContactPK: mess2PK})
	require.NoError(t, err)

	grpInf2, err := protocol2.GroupInfo(ctx, &protocoltypes.GroupInfo_Request{ContactPK: mess1PK})
	require.NoError(t, err)

	require.Equal(t, grpInf1.Group.PublicKey, grpInf2.Group.PublicKey)

	_, err = protocol1.ActivateGroup(ctx, &protocoltypes.ActivateGroup_Request{GroupPK: grpInf1.Group.PublicKey})
	require.NoError(t, err)

	_, err = protocol2.ActivateGroup(ctx, &protocoltypes.ActivateGroup_Request{GroupPK: grpInf2.Group.PublicKey})
	require.NoError(t, err)

	time.Sleep(time.Second * 3)

	_, err = svc1.CloseAccount(ctx, &bertyaccount.CloseAccount_Request{})
	require.NoError(t, err)

	time.Sleep(time.Second * 1)

	um1 := &messengertypes.AppMessage_UserMessage{Body: "hey1"}
	um1B, err := proto.Marshal(um1)
	require.NoError(t, err)

	_, err = messenger2.Interact(ctx, &messengertypes.Interact_Request{
		Type:                  messengertypes.AppMessage_TypeUserMessage,
		Payload:               um1B,
		ConversationPublicKey: base64.RawURLEncoding.EncodeToString(grpInf1.Group.PublicKey),
	})
	require.NoError(t, err)

	time.Sleep(time.Second * 10)
	require.Equal(t, 1, dispatcher.Len([]byte(svc1Token)))

	decrypted, err := bertyaccount.PushDecryptStandalone(svc1RootDir, base64.StdEncoding.EncodeToString(dispatcher.Shift([]byte(svc1Token))))
	require.NoError(t, err)

	require.Equal(t, bertyaccount.DecryptedPush_Message.String(), decrypted.PushType.String())
	require.Equal(t, svc1Account1, decrypted.AccountID)
	require.Equal(t, base64.RawURLEncoding.EncodeToString(grpInf1.Group.PublicKey), decrypted.ConversationPublicKey)
	require.Equal(t, fmt.Sprintf("{\"message\":\"hey1\"}"), decrypted.PayloadAttrsJSON)
	// TODO:
	// require.Equal(t, svc1Account1, decrypted.MemberDisplayName)
}

package network

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func guestPolicyFixture(t *testing.T) (*GuestPolicyEngine, GuestPolicyRequest, *string, *int) {
	t.Helper()
	table := ""
	writes := 0
	e := &GuestPolicyEngine{Root: t.TempDir(), Run: func(ctx context.Context, input string, args ...string) (string, error) {
		if input == "" {
			return table, nil
		}
		writes++
		if strings.HasPrefix(input, "delete table") && !strings.Contains(input, "add table") {
			table = ""
		} else {
			// The compiler produces nft input; the fake kernel exposes its table form.
			name := regexp.MustCompile(`add table ip (\w+)`).FindStringSubmatch(input)[1]
			identity := regexp.MustCompile(`add table ip \w+ \{ comment "([^"]+)"`).FindStringSubmatch(input)[1]
			marker := regexp.MustCompile(`accept comment "([^"]+)"`).FindStringSubmatch(input)[1]
			table = fmt.Sprintf("table ip %s {\n comment %q\n chain input { accept comment %q }\n}\n", name, identity, marker)
		}
		return "", nil
	}}
	id := substrate.OperationIdentity{ActionID: "policy-action", TopologyUID: "top", OwnerUID: "owner", ResourceUID: "policy", Generation: 1}
	node := id
	node.ActionID = "node-action"
	node.ResourceUID = "node"
	req := GuestPolicyRequest{Operation: "apply", Node: node, Identity: id, Owner: "lab/fw", Spec: driver.RulesetSpec{Owner: "lab/fw", Family: driver.FamilyIPv4, DefaultInput: driver.VerdictDrop, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop}}
	return e, req, &table, &writes
}
func TestGuestPolicyValidationRejectsConflictingIdentity(t *testing.T) {
	_, req, _, _ := guestPolicyFixture(t)
	require.NoError(t, req.Validate())
	req.Node.TopologyUID = "foreign"
	require.Error(t, req.Validate())
}
func TestGuestPolicyLostResponseAndColdHostRetry(t *testing.T) {
	e, req, _, writes := guestPolicyFixture(t)
	first, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	// Response is discarded; reconstruction reads durable journal and kernel.
	cold := &GuestPolicyEngine{Root: e.Root, Run: e.Run}
	next, err := cold.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, first, next)
	require.Equal(t, 1, *writes)
}
func TestGuestPolicyRebootRetainsDeleteTombstoneAndRejectsOldGeneration(t *testing.T) {
	e, req, table, _ := guestPolicyFixture(t)
	_, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	req.Operation = "delete"
	_, err = e.Execute(context.Background(), req)
	require.NoError(t, err)
	*table = "" // Kernel state is gone after guest restart; journal remains.
	e = &GuestPolicyEngine{Root: e.Root, Run: e.Run}
	req.Operation = "apply"
	_, err = e.Execute(context.Background(), req)
	require.Error(t, err)
	req.Identity.Generation++
	_, err = e.Execute(context.Background(), req)
	require.NoError(t, err)
	req.Identity.Generation--
	for _, op := range []string{"apply", "delete", "observe"} {
		req.Operation = op
		_, err = e.Execute(context.Background(), req)
		require.Error(t, err)
	}
}
func TestGuestPolicyFailureAfterKernelCommitRecoversWithoutSecondMutation(t *testing.T) {
	e, req, _, writes := guestPolicyFixture(t)
	run := e.Run
	e.Run = func(ctx context.Context, input string, args ...string) (string, error) {
		out, err := run(ctx, input, args...)
		if input != "" {
			return out, errors.New("lost nft reply")
		}
		return out, err
	}
	_, err := e.Execute(context.Background(), req)
	require.Error(t, err)
	e = &GuestPolicyEngine{Root: e.Root, Run: run}
	_, err = e.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, *writes)
}

func TestGuestPolicyDeletedObservationVerifiesAbsenceWithoutResurrection(t *testing.T) {
	e, r, _, _ := guestPolicyFixture(t)
	_, err := e.Execute(context.Background(), r)
	require.NoError(t, err)
	r.Operation = "delete"
	_, err = e.Execute(context.Background(), r)
	require.NoError(t, err)
	r.Operation = "observe"
	_, err = e.Execute(context.Background(), r)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
	r.Operation = "apply"
	_, err = e.Execute(context.Background(), r)
	require.Error(t, err)
	r.Identity.Generation++
	r.Operation = "discover"
	_, err = e.Execute(context.Background(), r)
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound), "%v", err)
}

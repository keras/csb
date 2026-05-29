package csb

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDynamicPublish_PassthroughForExplicitHostPort(t *testing.T) {
	in := []string{"8080:8080", "127.0.0.1:5432:5432", "5432:5432/tcp"}
	out, env, err := resolveDynamicPublish(in)
	require.NoError(t, err)
	assert.Equal(t, in, out)
	assert.Empty(t, env)
}

func TestResolveDynamicPublish_RewritesBarePort(t *testing.T) {
	out, env, err := resolveDynamicPublish([]string{"6080"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, env, 1)

	assert.Equal(t, "CSB_PUBLISH_6080", env[0][0])
	hostPort, err := strconv.Atoi(env[0][1])
	require.NoError(t, err)
	assert.Greater(t, hostPort, 0)
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d:6080", hostPort), out[0])
}

func TestResolveDynamicPublish_PreservesProtoSuffix(t *testing.T) {
	out, env, err := resolveDynamicPublish([]string{"5353/udp"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, env, 1)

	assert.Equal(t, "CSB_PUBLISH_5353", env[0][0])
	assert.True(t, strings.HasSuffix(out[0], ":5353/udp"), "got %q", out[0])
}

func TestResolveDynamicPublish_MixedSpecs(t *testing.T) {
	out, env, err := resolveDynamicPublish([]string{"8080:8080", "6080", "127.0.0.1:5432:5432"})
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.Len(t, env, 1)

	assert.Equal(t, "8080:8080", out[0])
	assert.Equal(t, "127.0.0.1:5432:5432", out[2])
	assert.Equal(t, "CSB_PUBLISH_6080", env[0][0])
}

func TestResolveDynamicPublish_AllocatesDistinctPorts(t *testing.T) {
	out, env, err := resolveDynamicPublish([]string{"6080", "5901"})
	require.NoError(t, err)
	require.Len(t, env, 2)
	assert.NotEqual(t, env[0][1], env[1][1])
	assert.NotEqual(t, out[0], out[1])
}

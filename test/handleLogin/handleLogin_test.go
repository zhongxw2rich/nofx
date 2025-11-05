// @Target(handleLogin)
package handleLogin

import (
	"testing"

	"nofx/test/harness"
)

// HandleLoginTest 嵌入 BaseTest，可按需重写 Before/After 钩子
type HandleLoginTest struct {
	harness.BaseTest
}

// Before 在每个用例执行前被调用，先调用父类的 Before 来做统一准备/清理
func (rt *HandleLoginTest) Before(t *testing.T) {
	rt.BaseTest.Before(t)
	if rt.Env != nil {
		t.Logf("TestEnv API URL: %s", rt.Env.URL())
	} else {
		t.Log("Warning: Env is nil in Before")
	}
}

// After 可选的清理/断言
func (rt *HandleLoginTest) After(t *testing.T) {
	// no-op
}

// @RunWith(case01)
func TestHandleLogin(t *testing.T) {
	rt := &HandleLoginTest{}
	harness.RunCase(t, rt)
}

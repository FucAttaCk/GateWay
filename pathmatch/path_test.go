package pathmatch

import (
	"regexp"
	"testing"
)

func TestStatic(t *testing.T) {
	static, err := regexp.Compile(`.\.(png|ico|gif|jpg|jpeg|css|js)$`)
	if nil != err {
		panic("compile error")
	}
	match := static.MatchString("/umi.74d4d8a0.css")

	b := static.MatchString("/scene/3/sub-scene/4")

	idx, err := regexp.Compile(`\w`)
	matchString := idx.MatchString("/umi.74d4d8a0.css")

	t.Log(b)
	t.Log(matchString)
	t.Log(match)
}

package permission

// 白盒测试：permission 包内部一致性。
// 目的：保证 Definitions / All / IsValid / DescriptionByCode 互相对齐，
// code 唯一、description 非空、All 是 Definitions 的派生。

import "testing"

func TestDefinitions_AllAndDefinitionsAligned(t *testing.T) {
	if len(All) != len(Definitions) {
		t.Errorf("len(All)=%d, len(Definitions)=%d，两者必须一致", len(All), len(Definitions))
	}
}

func TestDefinitions_AllCodeNonEmptyAndUnique(t *testing.T) {
	seen := make(map[string]bool, len(Definitions))
	for i, d := range Definitions {
		if d.Code == "" {
			t.Errorf("Definitions[%d].Code 为空", i)
		}
		if seen[d.Code] {
			t.Errorf("Definitions 含重复 code: %q", d.Code)
		}
		seen[d.Code] = true
	}
}

func TestDefinitions_AllDescriptionsNonEmpty(t *testing.T) {
	for i, d := range Definitions {
		if d.Description == "" {
			t.Errorf("Definitions[%d] (%q) 的 description 为空", i, d.Code)
		}
		if d.DescriptionEN == "" {
			t.Errorf("Definitions[%d] (%q) 的英文 description 为空", i, d.Code)
		}
	}
}

func TestIsValid_AcceptsEveryDefinitionCode(t *testing.T) {
	for _, d := range Definitions {
		if !IsValid(d.Code) {
			t.Errorf("IsValid(%q) = false，Definition 中存在但 IsValid 拒绝", d.Code)
		}
	}
}

func TestIsValid_RejectsUnknown(t *testing.T) {
	if IsValid("nonexistent_xxx") {
		t.Error("IsValid 应拒绝未知 code")
	}
}

func TestDescriptionByCode_ReturnsNonEmptyForKnown(t *testing.T) {
	for _, d := range Definitions {
		got := DescriptionByCode(d.Code)
		if got == "" {
			t.Errorf("DescriptionByCode(%q) 为空", d.Code)
		}
		if got != d.Description {
			t.Errorf("DescriptionByCode(%q) = %q, want %q", d.Code, got, d.Description)
		}
	}
}

func TestDescriptionByCode_EmptyForUnknown(t *testing.T) {
	if got := DescriptionByCode("nonexistent_xxx"); got != "" {
		t.Errorf("DescriptionByCode(unknown) = %q, want empty", got)
	}
}

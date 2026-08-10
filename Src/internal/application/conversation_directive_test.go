package application

import "testing"

func TestResolveConversationDirectiveUsesExplicitUtterancesOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content    string
		action     conversationDirectiveAction
		clearQueue bool
		matched    bool
	}{
		{content: "取消当前任务", action: conversationDirectiveCancel, matched: true},
		{content: "请帮我暂停当前任务一下", action: conversationDirectivePause, matched: true},
		{content: "取消并清空队列", action: conversationDirectiveCancel, clearQueue: true, matched: true},
		{content: "继续当前任务", action: conversationDirectiveContinue, matched: true},
		{content: "继续当前任务。不要再检索，补足现有报告并核对总字数", action: conversationDirectiveContinue, matched: true},
		{content: "继续", matched: false},
		{content: "Retry current task", action: conversationDirectiveRetry, matched: true},
		{content: "不要取消当前任务", matched: false},
		{content: "解释一下取消当前任务和暂停当前任务的区别", matched: false},
		{content: "实现一个停止当前任务时保存检查点的功能", matched: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.content, func(t *testing.T) {
			t.Parallel()
			directive, matched := resolveConversationDirective(test.content)
			if matched != test.matched {
				t.Fatalf("matched=%v want=%v directive=%#v", matched, test.matched, directive)
			}
			if !matched {
				return
			}
			if directive.Action != test.action || directive.ClearQueue != test.clearQueue {
				t.Fatalf("directive=%#v", directive)
			}
		})
	}
}

func TestContinuationUpdateKeepsTheRequirementSeparateFromTheControlPrefix(t *testing.T) {
	directive, matched := resolveConversationDirective("继续当前任务。不要再检索，补足现有报告并核对总字数")
	if !matched || directive.Action != conversationDirectiveContinue || directive.Update != "不要再检索，补足现有报告并核对总字数" {
		t.Fatalf("directive=%#v matched=%v", directive, matched)
	}
}

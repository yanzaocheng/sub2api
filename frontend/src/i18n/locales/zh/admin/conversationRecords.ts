export default {
  conversationRecords: {
    title: '对话记录',
    description: '查看用户与模型的对话内容，一场对话一行，点击可查看完整往来。需在「系统设置 → 功能开关」中开启保存。',
    disabledBanner: {
      title: '对话记录当前未开启',
      description: '不会保存新的对话；已保存的内容仍可查看和删除。',
      action: '前往系统设置'
    },
    filters: {
      all: '全部',
      q: '关键词',
      qPlaceholder: '搜索对话内容',
      user: '用户',
      userPlaceholder: '用户邮箱',
      model: '模型',
      modelPlaceholder: '模型名称'
    },
    columns: {
      lastActive: '最近对话',
      user: '用户',
      model: '模型',
      conversation: '对话',
      turns: '轮数'
    },
    startedAt: '开始于 {time}',
    view: '查看',
    empty: '暂无对话记录',
    loadFailed: '加载对话记录失败',
    selectRow: '选择 {user} 的对话',
    deleteSelected: '删除所选（{count}）',
    clearAll: '全部清空',
    deleted: '已删除 {count} 条记录',
    deleteFailed: '删除失败',
    detail: {
      title: '对话详情',
      turns: '共 {count} 轮',
      truncated: '仅显示最近 {shown} 轮，该对话共 {total} 轮',
      systemPrompt: '系统提示词',
      stream: '流式',
      delete: '删除这场对话'
    },
    roles: {
      user: '用户',
      assistant: '模型',
      tool: '工具结果',
      system: '系统',
      note: '提示'
    },
    deleteConfirm: {
      title: '删除对话',
      message: '将删除这场对话的全部记录，且无法恢复。'
    },
    bulkDeleteConfirm: {
      title: '删除所选对话',
      message: '将删除所选 {count} 场对话的全部记录，且无法恢复。'
    },
    clearConfirm: {
      title: '清空对话记录',
      message: '将删除所有用户的全部对话记录，且无法恢复。'
    },
    settings: {
      title: '用户对话记录',
      description: '保存用户与模型的对话内容（用户输入与模型回复），可在后台「对话记录」中查看。',
      viewLink: '查看对话记录',
      privacyNote:
        '开启后，经网关转发的聊天内容会以明文保存在数据库中，其中可能包含用户的隐私或敏感信息。请只在确有需要时开启，并按当地法规告知用户。',
      enabled: '保存对话记录',
      enabledHint:
        '开启后保存聊天类请求（Claude Messages、OpenAI Chat 与 Responses、Gemini）的输入和回复。图片等附件只记录占位符，思考过程不保存。默认关闭。',
      retentionDays: '保留天数',
      daysUnit: '天',
      retentionDaysHint: '超过该天数的记录会被自动删除；0 表示永久保留。',
      retentionInvalid: '保留天数需为 0 到 {max} 之间的整数',
      saved: '对话记录设置已保存',
      saveFailed: '保存对话记录设置失败',
      loadFailed: '加载对话记录设置失败',
      retry: '重试',
      unsaved: '有未保存的修改，点击「保存」后才会生效'
    }
  }
}

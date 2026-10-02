export default {
  conversationRecords: {
    title: 'Conversation Records',
    description: 'One row per conversation between users and models. Turn on recording in System Settings → Feature Switches.',
    disabledBanner: {
      title: 'Conversation recording is off',
      description: 'New conversations are not saved. Records that were already saved can still be viewed and deleted.',
      action: 'Go to System Settings'
    },
    filters: {
      all: 'All',
      q: 'Keyword',
      qPlaceholder: 'Search conversation content',
      user: 'User',
      userPlaceholder: 'User email',
      model: 'Model',
      modelPlaceholder: 'Model name'
    },
    columns: {
      lastActive: 'Last active',
      user: 'User',
      model: 'Model',
      conversation: 'Conversation',
      turns: 'Turns'
    },
    startedAt: 'Started {time}',
    view: 'View',
    empty: 'No conversation records yet',
    loadFailed: 'Failed to load conversation records',
    selectRow: 'Select the conversation of {user}',
    deleteSelected: 'Delete selected ({count})',
    clearAll: 'Clear all',
    deleted: 'Deleted {count} records',
    deleteFailed: 'Failed to delete',
    detail: {
      title: 'Conversation',
      turns: '{count} turns',
      truncated: 'Showing the latest {shown} turns of {total}',
      systemPrompt: 'System prompt',
      stream: 'Streamed',
      delete: 'Delete this conversation'
    },
    roles: {
      user: 'User',
      assistant: 'Model',
      tool: 'Tool result',
      system: 'System',
      note: 'Note'
    },
    deleteConfirm: {
      title: 'Delete conversation',
      message: 'All records of this conversation will be deleted. This cannot be undone.'
    },
    bulkDeleteConfirm: {
      title: 'Delete selected conversations',
      message: 'All records of the {count} selected conversations will be deleted. This cannot be undone.'
    },
    clearConfirm: {
      title: 'Clear conversation records',
      message: 'Every conversation record of every user will be deleted. This cannot be undone.'
    },
    settings: {
      title: 'User conversation records',
      description:
        'Save what users and models say to each other (user input and model replies). Browse them under Conversation Records in the admin menu.',
      viewLink: 'View conversation records',
      privacyNote:
        'When enabled, chat content relayed by the gateway is stored in the database as plain text and may contain private or sensitive information. Enable it only when you need it, and inform your users as required by local regulations.',
      enabled: 'Save conversation records',
      enabledHint:
        'Saves the input and reply of chat requests (Claude Messages, OpenAI Chat and Responses, Gemini). Attachments such as images are stored as placeholders and reasoning is not saved. Off by default.',
      retentionDays: 'Retention',
      daysUnit: 'days',
      retentionDaysHint: 'Records older than this are deleted automatically. 0 keeps them forever.',
      retentionInvalid: 'Retention must be a whole number between 0 and {max}',
      saved: 'Conversation record settings saved',
      saveFailed: 'Failed to save conversation record settings',
      loadFailed: 'Failed to load conversation record settings',
      retry: 'Retry',
      unsaved: 'You have unsaved changes. They only take effect after you click Save.'
    }
  }
}

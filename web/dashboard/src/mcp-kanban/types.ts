export type KanbanToolName =
  | "open_kanban"
  | "create_kanban_card"
  | "update_kanban_card"
  | "append_kanban_worklog"
  | "move_kanban_card"
  | "delete_kanban_card";

export type ToolContent = { type: string; text?: string };

export type ToolResult<T = Record<string, unknown>> = {
  content?: ToolContent[];
  structuredContent?: T;
  isError?: boolean;
  _meta?: Record<string, unknown>;
};

export type KanbanAttachment = {
  id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
};

export type KanbanCard = {
  board_id: string;
  board_title: string;
  board_revision: string;
  column_id: string;
  column_title: string;
  column_role?: string;
  card_id: string;
  title: string;
  details_markdown: string;
  due_date?: string;
  tags: string[];
  color?: string;
  created_at?: string;
  updated_at?: string;
  attachments?: KanbanAttachment[];
};

export type KanbanColumn = {
  column_id: string;
  title: string;
  role?: string;
  cards: KanbanCard[];
};

export type KanbanBoard = {
  board_id: string;
  title: string;
  revision: string;
  intake_column_id?: string;
  columns: KanbanColumn[];
};

export type KanbanBoardSummary = {
  board_id: string;
  title: string;
  default: boolean;
  revision: string;
  intake_column_id?: string;
  total_card_count: number;
  active_card_count: number;
};

export type KanbanPermissions = {
  can_read: boolean;
  can_write: boolean;
  can_delete: boolean;
};

export type KanbanSnapshot = {
  state_version: string;
  boards: KanbanBoardSummary[];
  selected_board: KanbanBoard | null;
  permissions: KanbanPermissions;
};

export type KanbanCardResult = { card: KanbanCard; state_version: string };
export type KanbanDeleteResult = {
  board_id: string;
  card_id: string;
  board_revision: string;
  state_version: string;
  deleted: boolean;
};

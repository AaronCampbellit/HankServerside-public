type SidebarDividerToggleProps = {
  expanded: boolean;
  controls: string;
  expandLabel: string;
  collapseLabel: string;
  className?: string;
  onToggle: () => void;
};

export function SidebarDividerToggle({ expanded, controls, expandLabel, collapseLabel, className = "", onToggle }: SidebarDividerToggleProps) {
  const label = expanded ? collapseLabel : expandLabel;
  return (
    <button
      className={`sidebar-divider-toggle ${className}`}
      type="button"
      aria-label={label}
      title={label}
      aria-expanded={expanded}
      aria-controls={controls}
      onClick={onToggle}
    >
      <span aria-hidden="true">{expanded ? "‹" : "›"}</span>
    </button>
  );
}

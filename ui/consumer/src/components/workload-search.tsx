/**
 * Page-level search box for the workloads list.
 *
 * Lives on the page rather than inside the table because both views search:
 * the cards grid has no `DataTable` to borrow `DataTable.Search` from, and a
 * second, independent box would mean a query vanished whenever the user
 * switched views. One controlled input, one piece of state, filtered once in
 * `WorkloadList`.
 *
 * Markup and classes mirror the portal's own `TableSearchInput`
 * (`app/components/table/components/toolbar.tsx`) so it reads as the same
 * control — which also keeps every class string inside the host's compiled
 * CSS, since a plugin has no Tailwind build of its own.
 */
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { InputWithAddons } from '@datum-cloud/datum-ui/input-with-addons';
import { SearchIcon, XIcon } from 'lucide-react';

export function WorkloadSearch({
  value,
  onChange,
  placeholder = 'Search',
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  return (
    <div className="w-full min-w-full flex-1 rounded-md sm:max-w-3xs md:min-w-80">
      <InputWithAddons
        type="text"
        placeholder={placeholder}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-label="Search workloads"
        data-testid="compute-plugin-workload-search"
        containerClassName="h-9 bg-transparent"
        className="placeholder:text-secondary text-secondary h-full bg-transparent text-xs placeholder:text-xs md:text-xs dark:text-white dark:placeholder:text-white"
        leading={
          <Icon icon={SearchIcon} size={14} className="text-icon-quaternary dark:text-white" />
        }
        trailing={
          value ? (
            <Button
              type="quaternary"
              theme="borderless"
              size="icon"
              onClick={() => onChange('')}
              className="hover:text-destructive text-icon-quaternary size-4 p-0 hover:bg-transparent dark:text-white">
              <Icon icon={XIcon} size={14} />
              <span className="sr-only">Clear search</span>
            </Button>
          ) : undefined
        }
      />
    </div>
  );
}

import {
  Box,
  Button,
  Center,
  CloseButton,
  Group,
  Indicator,
  Popover,
  Text,
  TextInput
} from "@mantine/core";
import type { Column, Table } from "@tanstack/react-table";
import type { LegacyFeatures } from "@tanstack/react-table/legacy";
import { CaretDown, MagnifyingGlass } from "phosphor-react";
import { CSSProperties, useState } from "react";
import { createStyles } from "../../../mantine/createStyles";
import { useGetServersQuery } from "../../../state/api";

const stringContains = (first: string, second: string): boolean => {
  return first.toLowerCase().includes(second.toLowerCase());
};

const useStyles = createStyles((theme) => {
  const border = `1px solid ${
    theme.colorScheme === "dark" ? theme.colors.dark[4] : theme.colors.gray[3]
  }`;

  return {
    header: {
      padding: 6,
      textTransform: "none"
    },
    search: {
      borderTop: border,
      borderBottom: border,
      background:
        theme.colorScheme === "dark"
          ? theme.colors.dark[5]
          : theme.colors.gray[0]
    },
    container: {
      maxHeight: 200,
      overflow: "auto",
      textTransform: "none"
    },
    button: {
      ["&:hover"]: {
        backgroundColor:
          theme.colorScheme === "dark"
            ? theme.colors.dark[4]
            : theme.colors.gray[0]
      }
    }
  };
});

interface FacetFilterProps<
  TData extends object,
  TValue extends string = string
> {
  placeholder: string;
  column: Column<LegacyFeatures, TData, TValue>;
  table: Table<LegacyFeatures, TData>;
  Entry: React.FC<FacetEntryProps>;
}

export default function FacetFilter<
  TData extends object,
  TValue extends string = string
>({ placeholder, column, table, Entry }: FacetFilterProps<TData, TValue>) {
  const [filter, setFilter] = useState("");
  const [opened, setOpened] = useState(false);

  const options = Array.from(column.getFacetedUniqueValues().keys());
  const filteredOptions = options.filter((x) => stringContains(x, filter));

  const { classes, theme } = useStyles();

  // Entries are rendered directly instead of through useVirtualizer. The
  // list holds the distinct facet values of one column (dozens at most,
  // capped at 200px), so virtualization buys nothing here, and the nested
  // virtualizer never measured its scroll element inside the popover with
  // react-virtual 3.14 (range stayed null, no items rendered).
  const filterValue = (column.getFilterValue() ?? []) as string[];

  const buttonColor =
    theme.colorScheme === "dark"
      ? filterValue.length > 0
        ? "brand.2"
        : "dark.0"
      : filterValue.length > 0
        ? "brand.4"
        : "gray.7";

  return (
    <Popover
      width={200}
      trapFocus
      position="bottom"
      withArrow
      shadow="md"
      opened={opened}
      onChange={setOpened}
      styles={{ dropdown: { padding: 0 } }}>
      <Popover.Target>
        <Button
          variant="subtle"
          size="xs"
          className={classes.button}
          tt="uppercase"
          color={buttonColor}
          onClick={() => setOpened((o) => !o)}
          rightSection={<CaretDown weight="bold" />}>
          {placeholder}
        </Button>
      </Popover.Target>
      <Popover.Dropdown>
        <Group justify="space-between" className={classes.header}>
          <Text fw="normal" size="xs" color="dark">
            Filter {placeholder}
          </Text>
          <CloseButton
            onClick={() => setOpened(false)}
            aria-label="Close modal"
            color="dark"
            iconSize={12}
          />
        </Group>
        <TextInput
          data-autofocus
          leftSection={<MagnifyingGlass weight="bold" />}
          className={classes.search}
          variant="unstyled"
          value={filter}
          size="xs"
          onChange={(e) => setFilter(e.currentTarget.value)}
          placeholder="Filter..."
          rightSection={
            column.getIsFiltered() && (
              <Button
                style={{ marginRight: 25, fontWeight: "normal" }}
                size="xs"
                variant="default"
                color="brand"
                disabled={filterValue.length === 0}
                onClick={() => {
                  column.setFilterValue([]);
                  setFilter("");
                }}>
                Reset
              </Button>
            )
          }
        />

        <div className={classes.container}>
          {filteredOptions.map((option) => {
            const selected = filterValue.includes(option);
            return (
              <Entry
                key={option}
                style={{ height: 30 }}
                entry={option}
                selected={selected}
                onClick={(entry) =>
                  selected
                    ? column.setFilterValue(
                        filterValue.filter((x) => x !== entry)
                      )
                    : column.setFilterValue([...filterValue, entry])
                }
              />
            );
          })}

          {filteredOptions.length === 0 && (
            <Center py="xs">
              <Text color="dimmed" size="xs">
                No Results
              </Text>
            </Center>
          )}
        </div>
      </Popover.Dropdown>
    </Popover>
  );
}

const useFacetStyles = createStyles((theme) => {
  const border = `1px solid ${
    theme.colorScheme === "dark" ? theme.colors.dark[4] : theme.colors.gray[3]
  }`;

  return {
    entry: {
      ...theme.fn.focusStyles(),
      height: 30,
      position: "relative",
      display: "flex",
      alignItems: "center",
      justifyContent: "start",
      padding: "0 8px",
      cursor: "pointer",
      borderBottom: border,
      userSelect: "none",
      ["&:hover, &:focus"]: {
        backgroundColor:
          theme.colorScheme === "dark"
            ? theme.colors.dark[7]
            : theme.colors.gray[0]
      }
    },
    entrySelected: {
      backgroundColor:
        theme.colorScheme === "dark"
          ? theme.colors.dark[7]
          : theme.colors.gray[0]
    },
    indicator: {
      width: 2,
      height: "80%",
      position: "absolute",
      left: 0,
      backgroundColor:
        theme.colorScheme === "dark"
          ? theme.colors.brand[3]
          : theme.colors.brand[4],
      borderRadius: theme.radius.xl
    }
  };
});

export interface FacetEntryProps {
  entry: string;
  selected: boolean;
  onClick: (entry: string) => void;
  style: CSSProperties;
}

export function ServerFacetEntry({
  entry,
  onClick,
  selected,
  style
}: FacetEntryProps) {
  const { classes, cx } = useFacetStyles();
  const { data: servers } = useGetServersQuery(null);
  const serverOnline = servers?.includes(entry) ?? false;

  return (
    <Box
      tabIndex={0}
      className={cx(classes.entry, { [classes.entrySelected]: selected })}
      style={style}
      onClick={() => onClick(entry)}>
      <div className={cx({ [classes.indicator]: selected })}></div>

      <Text fz={12} fw="normal" color="dark" style={{ marginLeft: 20 }}>
        <Indicator
          position="middle-start"
          offset={-16}
          size={6}
          color={serverOnline ? "green.6" : "gray"}>
          {entry}
        </Indicator>
      </Text>
    </Box>
  );
}

export function StandardFacetEntry({
  entry,
  onClick,
  selected,
  style
}: FacetEntryProps) {
  const { classes, cx } = useFacetStyles();
  return (
    <Box
      tabIndex={0}
      className={cx(classes.entry, { [classes.entrySelected]: selected })}
      style={style}
      onClick={() => onClick(entry)}>
      <div className={cx({ [classes.indicator]: selected })}></div>

      <Text fz={12} fw="normal" color="dark">
        {entry}
      </Text>
    </Box>
  );
}

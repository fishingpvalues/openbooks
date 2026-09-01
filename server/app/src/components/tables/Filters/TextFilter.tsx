import { Box, TextInput } from "@mantine/core";
import { useMantineColorScheme } from "@mantine/core";
import { getHotkeyHandler } from "@mantine/hooks";
import type { Column, Table } from "@tanstack/react-table";
import type { LegacyFeatures } from "@tanstack/react-table/legacy";
import { useEffect, useState } from "react";

interface TextFilterProps<TData extends object> {
  icon?: React.ReactNode;
  placeholder: string;
  column: Column<LegacyFeatures, TData, any>;
  table: Table<LegacyFeatures, TData>;
}

export function TextFilter<TData extends object>({
  icon,
  placeholder,
  column,
  table
}: TextFilterProps<TData>) {
  const [filterValue, setFilterValue] = useState(
    column.getFilterValue() as string
  );
  const { colorScheme } = useMantineColorScheme();

  useEffect(() => {
    column.setFilterValue(filterValue);
  }, [filterValue]);

  const styledIcon = (
    <Box
      component="span"
      style={{
        display: "flex",
        color:
          colorScheme === "dark"
            ? filterValue
              ? "var(--mantine-color-brand-3)"
              : "var(--mantine-color-dark-3)"
            : filterValue
              ? "var(--mantine-color-brand-4)"
              : "var(--mantine-color-dark-1)"
      }}>
      {icon}
    </Box>
  );

  return (
    <TextInput
      leftSection={styledIcon}
      size="xs"
      placeholder={placeholder}
      styles={{
        input: {
          ["&::placeholder"]: {
            color:
              colorScheme === "dark"
                ? "var(--mantine-color-dark-0)"
                : "var(--mantine-color-gray-7)",
            textTransform: "uppercase",
            fontWeight: "bold"
          }
        }
      }}
      variant="unstyled"
      onChange={(e) => setFilterValue(e.currentTarget.value)}
      value={filterValue}
      onKeyDown={getHotkeyHandler([["Escape", () => setFilterValue("")]])}
    />
  );
}

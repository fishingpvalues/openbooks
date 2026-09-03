import {
  ActionIcon,
  AppShell,
  Burger,
  Group,
  SegmentedControl,
  Text,
  Tooltip,
  useMantineColorScheme
} from "@mantine/core";
import { useLocalStorage } from "@mantine/hooks";
import {
  BellSimple,
  IdentificationBadge,
  MoonStars,
  Plugs,
  Sidebar as SidebarIcon,
  Sun
} from "phosphor-react";
import { createStyles } from "../../mantine/createStyles";
import { toggleDrawer } from "../../state/notificationSlice";
import { toggleSidebar } from "../../state/stateSlice";
import { useAppDispatch, useAppSelector } from "../../state/store";
import History from "./History";
import Jobs from "./Jobs";
import Library from "./Library";
import Wanted from "./Wanted";

const useStyles = createStyles((theme) => ({
  footer: {
    borderTop: `1px solid ${
      theme.colorScheme === "dark" ? theme.colors.dark[4] : theme.colors.gray[3]
    }`,
    paddingTop: theme.spacing.sm
  }
}));

// Four views: the existing history (one-shot searches) and library, plus
// the v5.3.0 acquisition layer (the wanted watchlist and the download
// job log). The localStorage key survives the tab-set change; a saved
// value that is no longer a tab falls back to history.
type SidebarTab = "history" | "wanted" | "jobs" | "books";
const TABS: SidebarTab[] = ["history", "wanted", "jobs", "books"];

export default function Sidebar() {
  const { classes } = useStyles();
  const { colorScheme, toggleColorScheme } = useMantineColorScheme();

  const dispatch = useAppDispatch();
  const connected = useAppSelector((store) => store.state.isConnected);
  const username = useAppSelector((store) => store.state.username);
  const opened = useAppSelector((store) => store.state.isSidebarOpen);

  const [index, setIndex] = useLocalStorage<SidebarTab>({
    key: "sidebar-state",
    defaultValue: "history"
  });
  const tab: SidebarTab = TABS.includes(index) ? index : "history";

  if (!opened) {
    return <></>;
  }

  return (
    <>
      <AppShell.Section p="sm">
        <Group justify="space-between">
          <Text fw={700} size="lg">
            OpenBooks
          </Text>
          <Group gap="xs">
            <Tooltip
              label={`OpenBooks server ${
                connected ? "connected" : "disconnected"
              }.`}>
              <ActionIcon
                disabled={!connected}
                onClick={() => dispatch(toggleDrawer())}>
                <BellSimple weight="bold" size={18} />
              </ActionIcon>
            </Tooltip>
            <Burger
              opened={opened}
              onClick={() => dispatch(toggleSidebar())}
              size="sm"
              hiddenFrom="sm"
            />
          </Group>
        </Group>

        <Text size="sm" color="dimmed">
          Download eBooks from IRC Highway
        </Text>

        <SegmentedControl
          size="sm"
          styles={(theme) => ({
            root: {
              marginTop: theme.spacing.md
            },
            label: {
              fontSize: theme.fontSizes.xs
            }
          })}
          value={tab}
          onChange={(value: SidebarTab) => setIndex(value)}
          data={[
            { label: "History", value: "history" },
            { label: "Wanted", value: "wanted" },
            { label: "Jobs", value: "jobs" },
            { label: "Library", value: "books" }
          ]}
          fullWidth
        />
      </AppShell.Section>

      <AppShell.Section grow p="xs" style={{ overflow: "auto" }}>
        {tab === "history" && <History />}
        {tab === "wanted" && <Wanted />}
        {tab === "jobs" && <Jobs />}
        {tab === "books" && <Library />}
      </AppShell.Section>

      <AppShell.Section className={classes.footer} p="sm">
        <Group justify="space-between" wrap="nowrap">
          <Group>
            {username ? (
              <>
                <IdentificationBadge size={24} />
                <Text
                  size="sm"
                  lineClamp={1}
                  style={{
                    maxWidth: 150,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap"
                  }}>
                  {username}
                </Text>
              </>
            ) : (
              <>
                <Plugs size={24} />
                <Text size="sm">Not connected.</Text>
              </>
            )}
          </Group>

          <Group align="flex-end" gap="xs">
            <ActionIcon onClick={() => toggleColorScheme()}>
              {colorScheme === "dark" ? (
                <Sun size={18} weight="bold" />
              ) : (
                <MoonStars size={18} weight="bold" />
              )}
            </ActionIcon>
            <ActionIcon onClick={() => dispatch(toggleSidebar())}>
              <SidebarIcon weight="bold" size={18} />
            </ActionIcon>
          </Group>
        </Group>
      </AppShell.Section>
    </>
  );
}

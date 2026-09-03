import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Center,
  Group,
  Loader,
  Menu,
  Stack,
  Text,
  Tooltip
} from "@mantine/core";
import { showNotification } from "@mantine/notifications";
import { Dispatch } from "@reduxjs/toolkit";
import { AnimatePresence, motion } from "motion/react";
import { Eraser, Eye, EyeSlash, MagnifyingGlass, Trash } from "phosphor-react";
import { useSelector } from "react-redux";
import { useCleanSearchCacheMutation, useGetSearchCacheQuery } from "../../state/api";
import {
  deleteHistoryItem,
  HistoryItem,
  selectHistory
} from "../../state/historySlice";
import { setActiveItem } from "../../state/stateSlice";
import { useAppDispatch, useAppSelector } from "../../state/store";
import { defaultAnimation } from "../../utils/animation";
import { useSidebarButtonStyle } from "./styles";

export default function History() {
  const history = useSelector(selectHistory);
  const activeTS =
    useAppSelector((store) => store.state.activeItem?.timestamp) ?? -1;
  const dispatch = useAppDispatch();

  return (
    <Stack gap="xs">
      <CacheStrip />
      <AnimatePresence mode="popLayout">
        {history.length > 0 ? (
          history.map((item: HistoryItem) => (
            <motion.div {...defaultAnimation} key={item.timestamp.toString()}>
              <HistoryCard
                activeTS={activeTS}
                key={item.timestamp.toString()}
                item={item}
                dispatch={dispatch}
              />
            </motion.div>
          ))
        ) : (
          <Center>
            <Text color="dimmed" size="sm">
              History is a mystery.
            </Text>
          </Center>
        )}
      </AnimatePresence>
    </Stack>
  );
}

type Props = {
  activeTS: number;
  item: HistoryItem;
  dispatch: Dispatch<any>;
};

function HistoryCard({ activeTS, item, dispatch }: Props) {
  const isActive = activeTS === item.timestamp;
  const { classes } = useSidebarButtonStyle({ isActive });

  const loading = !item.results?.length && !item.errors?.length;

  return (
    <Menu shadow="md">
      <Menu.Target>
        <Tooltip label={item.query} openDelay={1_000}>
          <Button
            classNames={classes}
            radius="sm"
            variant="outline"
            fullWidth
            leftSection={<MagnifyingGlass size={18} weight="bold" />}
            rightSection={
              loading ? (
                <Loader color="brand" size="xs" />
              ) : (
                <Badge color="brand" radius="sm" size="sm" variant="light">
                  {`${item.results?.length} RESULTS`}
                </Badge>
              )
            }>
            {item.query}
          </Button>
        </Tooltip>
      </Menu.Target>

      <Menu.Dropdown>
        {!isActive ? (
          <Menu.Item
            leftSection={<Eye size={18} weight="bold" />}
            onClick={() => dispatch(setActiveItem(item))}>
            Show Results
          </Menu.Item>
        ) : (
          <Menu.Item
            leftSection={<EyeSlash size={18} weight="bold" />}
            onClick={() => dispatch(setActiveItem(null))}>
            Hide Results
          </Menu.Item>
        )}

        <Menu.Item
          color="red"
          leftSection={<Trash size={18} weight="bold" />}
          onClick={() => dispatch(deleteHistoryItem(item.timestamp))}>
          Delete item
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );
}

// The v5.3.0 search-cache stats (entries / hits / misses / ttl) with a
// clean button. The cache is off by default (ttl 0s) - the strip then
// reports that plainly instead of implying caching is on.
function CacheStrip() {
  const { data, isLoading } = useGetSearchCacheQuery(null, {
    pollingInterval: 30_000
  });
  const [cleanSearchCache, { isLoading: cleaning }] =
    useCleanSearchCacheMutation();

  if (isLoading || !data) {
    return null;
  }

  const onClean = () => {
    cleanSearchCache().then(
      (res) =>
        showNotification({
          title: "Search cache cleared",
          message: `${res.data?.removed ?? 0} entries removed`,
          color: "yellow"
        }),
      (err: any) =>
        showNotification({
          title: "Cache clean failed",
          message: err?.data?.error ?? String(err),
          color: "red"
        })
    );
  };

  return (
    <Box
      style={{
        display: "flex",
        alignItems: "center",
        gap: 6,
        padding: "6px 8px",
        borderRadius: 6,
        backgroundColor: "rgba(128,128,128,0.08)",
        fontSize: 11
      }}>
      <Text size="xs" c="dimmed" style={{ flex: 1 }}>
        Search cache:{" "}
        <b style={{ color: "inherit" }}>{data.entries}</b> entries ·{" "}
        <b style={{ color: "inherit" }}>{data.hits}</b> hits ·{" "}
        <b style={{ color: "inherit" }}>{data.misses}</b> misses ·{" "}
        <b style={{ color: "inherit" }}>{data.ttl}</b>
      </Text>
      <Tooltip label="Empty the cache (POST /api/v1/search-cache/clean)">
        <ActionIcon
          size="xs"
          variant="subtle"
          color="brand"
          loading={cleaning}
          onClick={onClean}>
          <Eraser size={13} />
        </ActionIcon>
      </Tooltip>
    </Box>
  );
}

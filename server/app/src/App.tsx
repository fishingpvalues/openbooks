import {
  AppShell,
  MantineColorScheme,
  MantineProvider
} from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { createStyles } from "./mantine/createStyles";
import { mantineStylesTransform } from "./mantine/stylesTransform";
import NotificationDrawer from "./components/drawer/NotificationDrawer";
import Sidebar from "./components/sidebar/Sidebar";
import SearchPage from "./pages/SearchPage";
import { useAppSelector } from "./state/store";

const useStyles = createStyles((theme) => ({
  wrapper: {
    boxSizing: "border-box",
    display: "flex",
    flexWrap: "nowrap",
    maxHeight: "100vh",
    minHeight: "100vh",
    backgroundColor:
      theme.colorScheme === "dark"
        ? theme.colors.dark[8]
        : theme.colors.gray[0]
  }
}));

function MainContent() {
  // Rendered below the MantineProvider so useStyles() resolves the real
  // theme and colour scheme for the full-viewport background.
  const { classes } = useStyles();
  return (
    <div className={classes.wrapper}>
      <SearchPage />
      <NotificationDrawer />
    </div>
  );
}

export default function App() {
  const open = useAppSelector((state) => state.state.isSidebarOpen);

  return (
    <MantineProvider
      colorSchemeManager={{
        get: (defaultValue: MantineColorScheme) =>
          (localStorage.getItem("color-scheme") as MantineColorScheme) ||
          defaultValue,
        set: (value: MantineColorScheme) =>
          localStorage.setItem("color-scheme", value),
        subscribe: () => {},
        unsubscribe: () => {},
        clear: () => localStorage.removeItem("color-scheme")
      }}
      theme={{
        primaryColor: "brand",
        primaryShade: { light: 4, dark: 2 },
        colors: {
          brand: [
            "#e0ecff",
            "#b0c6ff",
            "#7e9fff",
            "#4c79ff",
            "#3366ff",
            "#0039e6",
            "#002db4",
            "#002082",
            "#001351",
            "#000621"
          ]
        },
        components: {
          ActionIcon: {
            defaultProps: {
              radius: "md",
              color: "brand"
            }
          }
        }
      }}
      stylesTransform={mantineStylesTransform}>
      <Notifications position="top-center" />
      <AppShell
        navbar={{
          width: { sm: 300 },
          breakpoint: "sm",
          collapsed: { desktop: !open, mobile: !open }
        }}
        padding={0}>
        <AppShell.Navbar>
          <Sidebar />
        </AppShell.Navbar>
        <AppShell.Main>
          <MainContent />
        </AppShell.Main>
      </AppShell>
    </MantineProvider>
  );
}

import { Link, useLocation } from 'react-router';
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarHeader,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar';
import { Activity, Box, HardDriveDownload, PowerOff, Settings } from 'lucide-react';
import clsx from 'clsx';

const items = [
  {
    title: 'Updates',
    url: '/',
    icon: HardDriveDownload,
  },
  {
    title: 'Channels',
    url: '/channels',
    icon: Box,
  },
  {
    title: 'Bundle metrics',
    url: '/bundle-metrics',
    icon: Activity,
  },
  {
    title: 'Settings',
    url: '/settings',
    icon: Settings,
  },
  {
    title: 'Logout',
    url: '/logout',
    icon: PowerOff,
  },
];

export function AppSidebar() {
  const location = useLocation();
  const currentPath = location.pathname;

  return (
    <Sidebar className="w-64 bg-white border-r border-gray-200">
      <SidebarHeader className="p-4 border-b">
        <h1 className="text-lg font-semibold">Expo Open OTA</h1>
      </SidebarHeader>
      <SidebarContent className="p-2">
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {items.map(item => {
                const isActive = currentPath === item.url;
                return (
                  <SidebarMenuItem key={item.title}>
                    <SidebarMenuButton asChild disabled={isActive}>
                      <Link
                        to={item.url}
                        onClick={e => {
                          if (isActive) {
                            e.preventDefault();
                          }
                        }}
                        className={clsx(
                          'flex items-center gap-2 px-4 py-2 rounded-lg transition',
                          isActive ? 'bg-gray-200 text-black' : 'text-gray-500 hover:bg-gray-100'
                        )}>
                        <item.icon className="w-5 h-5" />
                        <span>{item.title}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter />
    </Sidebar>
  );
}

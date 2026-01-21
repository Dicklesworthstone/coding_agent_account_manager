"use client";

import { motion } from "framer-motion";
import {
  Activity,
  ArrowUpRight,
  Users,
  Key,
  AlertCircle,
  CheckCircle2,
} from "lucide-react";
import { DashboardLayout } from "@/components";

interface StatCardProps {
  title: string;
  value: string | number;
  change?: string;
  trend?: "up" | "down" | "neutral";
  icon: React.ReactNode;
}

function StatCard({ title, value, change, trend, icon }: StatCardProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      className="rounded-xl border border-border bg-surface p-6"
    >
      <div className="flex items-start justify-between">
        <div>
          <p className="text-sm text-muted">{title}</p>
          <p className="mt-2 text-3xl font-semibold">{value}</p>
          {change && (
            <p
              className={`mt-1 text-sm ${
                trend === "up"
                  ? "text-success"
                  : trend === "down"
                    ? "text-danger"
                    : "text-muted"
              }`}
            >
              {change}
            </p>
          )}
        </div>
        <div className="rounded-lg bg-accent/10 p-3 text-accent">{icon}</div>
      </div>
    </motion.div>
  );
}

interface ActivityItem {
  id: string;
  type: "success" | "warning" | "error";
  message: string;
  time: string;
}

const recentActivity: ActivityItem[] = [
  {
    id: "1",
    type: "success",
    message: 'Profile "work@anthropic" activated',
    time: "2 min ago",
  },
  {
    id: "2",
    type: "success",
    message: "Token refreshed for Codex",
    time: "15 min ago",
  },
  {
    id: "3",
    type: "warning",
    message: "Gemini token expires in 2 hours",
    time: "1 hour ago",
  },
  {
    id: "4",
    type: "error",
    message: "Failed to sync with remote-dev machine",
    time: "3 hours ago",
  },
];

export default function DashboardPage() {
  return (
    <DashboardLayout>
      <div className="space-y-8">
        {/* Page Header */}
        <div>
          <h1 className="text-2xl font-semibold">Dashboard</h1>
          <p className="mt-1 text-muted">
            Overview of your AI coding assistant accounts
          </p>
        </div>

        {/* Stats Grid */}
        <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
          <StatCard
            title="Active Profiles"
            value={12}
            change="+2 this week"
            trend="up"
            icon={<Users className="h-6 w-6" />}
          />
          <StatCard
            title="API Calls Today"
            value="1,284"
            change="+12% vs yesterday"
            trend="up"
            icon={<Activity className="h-6 w-6" />}
          />
          <StatCard
            title="Active Credentials"
            value={8}
            change="All healthy"
            trend="neutral"
            icon={<Key className="h-6 w-6" />}
          />
          <StatCard
            title="Sync Status"
            value="3/3"
            change="All machines synced"
            trend="neutral"
            icon={<ArrowUpRight className="h-6 w-6" />}
          />
        </div>

        {/* Content Grid */}
        <div className="grid gap-6 lg:grid-cols-2">
          {/* Recent Activity */}
          <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: 0.1 }}
            className="rounded-xl border border-border bg-surface"
          >
            <div className="border-b border-border px-6 py-4">
              <h2 className="font-semibold">Recent Activity</h2>
            </div>
            <div className="divide-y divide-border">
              {recentActivity.map((item) => (
                <div
                  key={item.id}
                  className="flex items-start gap-3 px-6 py-4"
                >
                  {item.type === "success" ? (
                    <CheckCircle2 className="mt-0.5 h-5 w-5 text-success" />
                  ) : item.type === "warning" ? (
                    <AlertCircle className="mt-0.5 h-5 w-5 text-warning" />
                  ) : (
                    <AlertCircle className="mt-0.5 h-5 w-5 text-danger" />
                  )}
                  <div className="flex-1">
                    <p className="text-sm">{item.message}</p>
                    <p className="mt-1 text-xs text-muted">{item.time}</p>
                  </div>
                </div>
              ))}
            </div>
          </motion.div>

          {/* Quick Actions */}
          <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: 0.2 }}
            className="rounded-xl border border-border bg-surface"
          >
            <div className="border-b border-border px-6 py-4">
              <h2 className="font-semibold">Quick Actions</h2>
            </div>
            <div className="grid grid-cols-2 gap-4 p-6">
              <button className="flex flex-col items-center gap-2 rounded-lg border border-border bg-background p-4 text-sm transition-colors hover:border-accent hover:bg-accent/5">
                <Users className="h-6 w-6 text-accent" />
                <span>Switch Profile</span>
              </button>
              <button className="flex flex-col items-center gap-2 rounded-lg border border-border bg-background p-4 text-sm transition-colors hover:border-accent hover:bg-accent/5">
                <Key className="h-6 w-6 text-accent" />
                <span>Refresh Token</span>
              </button>
              <button className="flex flex-col items-center gap-2 rounded-lg border border-border bg-background p-4 text-sm transition-colors hover:border-accent hover:bg-accent/5">
                <ArrowUpRight className="h-6 w-6 text-accent" />
                <span>Sync Now</span>
              </button>
              <button className="flex flex-col items-center gap-2 rounded-lg border border-border bg-background p-4 text-sm transition-colors hover:border-accent hover:bg-accent/5">
                <Activity className="h-6 w-6 text-accent" />
                <span>View Logs</span>
              </button>
            </div>
          </motion.div>
        </div>
      </div>
    </DashboardLayout>
  );
}

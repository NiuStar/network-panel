import { Navbar } from "@/components/navbar";

export default function DefaultLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="relative flex flex-col min-h-screen apple-shell">
      <div className="apple-backdrop">
        <span className="apple-orb orb-a" />
        <span className="apple-orb orb-b" />
      </div>
      <Navbar />
      <main className="container mx-auto max-w-7xl px-4 sm:px-6 flex-grow pt-4 sm:pt-16 apple-content relative z-10">
        {children}
      </main>
    </div>
  );
}

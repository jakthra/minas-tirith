package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFakeFiles(t *testing.T) {
	f := NewFake("root/BeanieGenie")
	f.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/tasks/task-1.md": "# Task 1",
		"backlog/tasks/task-2.md": "# Task 2",
		"README.md":               "hello",
	})

	t.Run("get file on default branch", func(t *testing.T) {
		file, err := f.GetFile(context.Background(), "root/BeanieGenie", "backlog/tasks/task-1.md", "")
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if string(file.Content) != "# Task 1" || file.Path != "backlog/tasks/task-1.md" {
			t.Errorf("file = %+v, want seeded content and path", file)
		}
	})

	t.Run("get file on missing ref", func(t *testing.T) {
		if _, err := f.GetFile(context.Background(), "root/BeanieGenie", "README.md", "feature"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("get missing file", func(t *testing.T) {
		if _, err := f.GetFile(context.Background(), "root/BeanieGenie", "nope.md", "main"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("get file in missing repo", func(t *testing.T) {
		if _, err := f.GetFile(context.Background(), "root/other", "README.md", ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("list files recursively", func(t *testing.T) {
		infos, err := f.ListFiles(context.Background(), "root/BeanieGenie", "backlog", "main")
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		paths := make([]string, len(infos))
		for i, info := range infos {
			paths[i] = info.Path
		}
		if want := []string{"backlog/tasks/task-1.md", "backlog/tasks/task-2.md"}; !reflect.DeepEqual(paths, want) {
			t.Errorf("paths = %v, want %v", paths, want)
		}
	})

	t.Run("list all files", func(t *testing.T) {
		infos, err := f.ListFiles(ctx(), "root/BeanieGenie", "", "main")
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		if len(infos) != 3 {
			t.Errorf("infos = %+v, want all 3 files", infos)
		}
	})
}

func TestFakeBranchAndCommit(t *testing.T) {
	f := NewFake("root/BeanieGenie")
	f.SeedFiles("root/BeanieGenie", "main", map[string]string{
		"backlog/story.md": "# Story",
	})

	branch, err := f.CreateBranch(ctx(), "root/BeanieGenie", "agent/story-1", "main")
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if branch.Name != "agent/story-1" {
		t.Errorf("branch = %+v, want agent/story-1", branch)
	}

	t.Run("duplicate branch conflicts", func(t *testing.T) {
		_, err := f.CreateBranch(ctx(), "root/BeanieGenie", "agent/story-1", "main")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
			t.Errorf("error = %v, want 409 APIError", err)
		}
	})

	t.Run("branch from missing ref", func(t *testing.T) {
		if _, err := f.CreateBranch(ctx(), "root/BeanieGenie", "agent/x", "missing"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("commit writes files to the branch only", func(t *testing.T) {
		result, err := f.Commit(ctx(), "root/BeanieGenie", "agent/story-1", "Add tickets", []FileChange{
			{Path: "backlog/tickets/impl-1.md", Content: []byte("# Impl 1")},
			{Path: "backlog/story.md", Content: []byte("# Story refined")},
		})
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if result.SHA == "" {
			t.Error("commit produced no sha")
		}

		refined, err := f.GetFile(ctx(), "root/BeanieGenie", "backlog/story.md", "agent/story-1")
		if err != nil || string(refined.Content) != "# Story refined" {
			t.Errorf("branch file = %+v err=%v, want updated content", refined, err)
		}
		created, err := f.GetFile(ctx(), "root/BeanieGenie", "backlog/tickets/impl-1.md", "agent/story-1")
		if err != nil || string(created.Content) != "# Impl 1" {
			t.Errorf("created file = %+v err=%v, want new content", created, err)
		}

		// main keeps its state.
		original, err := f.GetFile(ctx(), "root/BeanieGenie", "backlog/story.md", "main")
		if err != nil || string(original.Content) != "# Story" {
			t.Errorf("main file = %+v err=%v, want unchanged content", original, err)
		}

		// The commit sha also resolves.
		viaSHA, err := f.GetFile(ctx(), "root/BeanieGenie", "backlog/story.md", result.SHA)
		if err != nil || string(viaSHA.Content) != "# Story refined" {
			t.Errorf("file via commit sha = %+v err=%v, want updated content", viaSHA, err)
		}
	})

	t.Run("commit to missing branch", func(t *testing.T) {
		_, err := f.Commit(ctx(), "root/BeanieGenie", "missing-branch", "m",
			[]FileChange{{Path: "a.md", Content: []byte("x")}})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("commit without changes", func(t *testing.T) {
		if _, err := f.Commit(ctx(), "root/BeanieGenie", "main", "m", nil); err == nil {
			t.Error("commit without changes accepted")
		}
	})
}

func TestFakeMergeRequestFlow(t *testing.T) {
	f := NewFake("root/BeanieGenie")
	_, err := f.CreateBranch(ctx(), "root/BeanieGenie", "agent/story-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	mr, err := f.CreateMergeRequest(ctx(), "root/BeanieGenie", "Refine story", "Tickets inside", "agent/story-1", "main")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	if mr.Number == 0 || mr.State != "open" || mr.Head != "agent/story-1" || mr.Base != "main" {
		t.Errorf("mr = %+v, want open MR head->base", mr)
	}

	t.Run("get merge request", func(t *testing.T) {
		got, err := f.GetMergeRequest(ctx(), "root/BeanieGenie", mr.Number)
		if err != nil {
			t.Fatalf("GetMergeRequest: %v", err)
		}
		if got != mr {
			t.Errorf("mr = %+v, want %+v", got, mr)
		}
	})

	t.Run("get missing merge request", func(t *testing.T) {
		if _, err := f.GetMergeRequest(ctx(), "root/BeanieGenie", 99); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})

	t.Run("comments", func(t *testing.T) {
		first, err := f.AddMergeRequestComment(ctx(), "root/BeanieGenie", mr.Number, "please adjust")
		if err != nil {
			t.Fatalf("AddMergeRequestComment: %v", err)
		}
		second, err := f.AddMergeRequestComment(ctx(), "root/BeanieGenie", mr.Number, "done")
		if err != nil {
			t.Fatalf("AddMergeRequestComment: %v", err)
		}
		comments, err := f.ListMergeRequestComments(ctx(), "root/BeanieGenie", mr.Number)
		if err != nil {
			t.Fatalf("ListMergeRequestComments: %v", err)
		}
		want := []Comment{first, second}
		if !reflect.DeepEqual(comments, want) {
			t.Errorf("comments = %+v, want %+v", comments, want)
		}

		// A returned list is a copy: mutating it must not corrupt the fake.
		comments[0].Body = "mutated"
		again, _ := f.ListMergeRequestComments(ctx(), "root/BeanieGenie", mr.Number)
		if again[0].Body != "please adjust" {
			t.Error("ListMergeRequestComments leaked its internal slice")
		}
	})

	t.Run("comment on missing merge request", func(t *testing.T) {
		if _, err := f.AddMergeRequestComment(ctx(), "root/BeanieGenie", 99, "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestFakeErr(t *testing.T) {
	injected := errors.New("provider unavailable")
	f := NewFake("root/BeanieGenie")
	f.Err = injected

	ctx := ctx()
	if _, err := f.GetFile(ctx, "root/BeanieGenie", "a.md", "main"); !errors.Is(err, injected) {
		t.Errorf("GetFile error = %v, want injected", err)
	}
	if _, err := f.ListFiles(ctx, "root/BeanieGenie", "", "main"); !errors.Is(err, injected) {
		t.Errorf("ListFiles error = %v, want injected", err)
	}
	if _, err := f.CreateBranch(ctx, "root/BeanieGenie", "b", "main"); !errors.Is(err, injected) {
		t.Errorf("CreateBranch error = %v, want injected", err)
	}
	if _, err := f.Commit(ctx, "root/BeanieGenie", "main", "m", []FileChange{{Path: "a", Content: []byte("x")}}); !errors.Is(err, injected) {
		t.Errorf("Commit error = %v, want injected", err)
	}
	if _, err := f.CreateMergeRequest(ctx, "root/BeanieGenie", "t", "d", "h", "main"); !errors.Is(err, injected) {
		t.Errorf("CreateMergeRequest error = %v, want injected", err)
	}
	if _, err := f.GetMergeRequest(ctx, "root/BeanieGenie", 1); !errors.Is(err, injected) {
		t.Errorf("GetMergeRequest error = %v, want injected", err)
	}
	if _, err := f.ListMergeRequestComments(ctx, "root/BeanieGenie", 1); !errors.Is(err, injected) {
		t.Errorf("ListMergeRequestComments error = %v, want injected", err)
	}
	if _, err := f.AddMergeRequestComment(ctx, "root/BeanieGenie", 1, "x"); !errors.Is(err, injected) {
		t.Errorf("AddMergeRequestComment error = %v, want injected", err)
	}
}

// compile-time check that the fake really is a client substitute for
// activity tests (AC: interface mockable).
var _ Client = (*Fake)(nil)

func ctx() context.Context { return context.Background() }
